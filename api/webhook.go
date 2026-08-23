// webhook.go
// بوت تيليجرام لاستنساخ حزم الملصقات - جاهز للنشر على Vercel (Go Serverless Function)
//
// طريقة الاستخدام:
//   1) ضع هذا الملف داخل مجلد api/  → api/webhook.go
//   2) أنشئ ملف go.mod في جذر المشروع:
//        module stickerbot
//        go 1.21
//        require github.com/go-telegram-bot-api/telegram-bot-api/v5 v5.5.1
//      ثم نفّذ: go mod tidy
//   3) أضف متغير البيئة على Vercel باسم TELEGRAM_BOT_TOKEN (توكن البوت من BotFather)
//   4) بعد النشر، فعّل الويب هوك عبر:
//      https://api.telegram.org/bot<TOKEN>/setWebhook?url=https://<your-vercel-domain>/api/webhook
//
// ملاحظة: التخزين هنا في الذاكرة (in-memory) لتبسيط الكود كما طلب المستخدم.
// بيئات Serverless قد تُعيد تدوير الـ instances، فإن أردت ثباتاً كاملاً للحالة
// بين الطلبات يُفضّل استبدال الخرائط في الأسفل بقاعدة بيانات خارجية (Redis مثلاً).

package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// ==================== المتغيرات العامة ====================

var (
	bot     *tgbotapi.BotAPI
	botOnce sync.Once

	statesMu sync.RWMutex
	states   = make(map[int64]*UserState)

	packsMu   sync.RWMutex
	userPacks = make(map[int64][]PackInfo)
)

// خطوات محادثة نسخ الحزمة
type Step int

const (
	StepNone Step = iota
	StepWaitingTitle
	StepWaitingShortName
	StepWaitingSticker
	StepWaitingContinueSticker
)

// حالة المستخدم أثناء عملية النسخ
type UserState struct {
	ChatID         int64
	Step           Step
	PackTitle      string
	RawShortName   string
	FinalShortName string
	SourceSetName  string
	SourceStickers []tgbotapi.Sticker
	NextIndex      int
}

// معلومات حزمة تم إنشاؤها (لعرضها في "حزماتي")
type PackInfo struct {
	Title string
	Name  string
}

const firstBatchSize = 15

// ==================== تهيئة البوت ====================

func initBot() {
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		log.Println("⚠️ لم يتم ضبط متغير البيئة TELEGRAM_BOT_TOKEN")
		return
	}
	b, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Printf("❌ خطأ أثناء تهيئة البوت: %v", err)
		return
	}
	bot = b
	log.Printf("✅ تم تسجيل الدخول باسم: @%s", bot.Self.UserName)
}

// ==================== نقطة الدخول (Vercel Handler) ====================

func Handler(w http.ResponseWriter, r *http.Request) {
	botOnce.Do(initBot)

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "Bot is running ✅")
		return
	}

	if bot == nil {
		http.Error(w, "Bot not initialized (check TELEGRAM_BOT_TOKEN)", http.StatusInternalServerError)
		return
	}

	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("🛑 panic تم اعتراضه ومنعه من إسقاط الخدمة: %v", rec)
		}
	}()

	var update tgbotapi.Update
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		log.Printf("❌ خطأ بفك تشفير الأوبديت: %v", err)
		w.WriteHeader(http.StatusOK)
		return
	}

	handleUpdate(update)

	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "OK")
}

// ==================== توجيه الأوبديت ====================

func handleUpdate(update tgbotapi.Update) {
	switch {
	case update.CallbackQuery != nil:
		handleCallbackQuery(update.CallbackQuery)
	case update.Message != nil:
		handleMessage(update.Message)
	}
}

// ==================== القائمة الرئيسية ====================

func mainMenuKeyboard() tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📦 حزماتي", "my_packs"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🔄 نسخ حزمة ملصقات", "clone_pack"),
		),
	)
}

func sendWelcome(chatID int64) {
	text := "👋 أهلاً بك في بوت نسخ حزم الملصقات!\n\nاختر أحد الخيارات التالية:"
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ReplyMarkup = mainMenuKeyboard()
	if _, err := bot.Send(msg); err != nil {
		log.Printf("❌ فشل إرسال رسالة الترحيب: %v", err)
	}
}

func sendMainMenu(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ReplyMarkup = mainMenuKeyboard()
	if _, err := bot.Send(msg); err != nil {
		log.Printf("❌ فشل إرسال القائمة الرئيسية: %v", err)
	}
}

// ==================== معالجة الأزرار (Callback Query) ====================

func handleCallbackQuery(cq *tgbotapi.CallbackQuery) {
	if cq.Message == nil {
		return
	}
	chatID := cq.Message.Chat.ID
	userID := cq.From.ID

	// الرد على الكولباك لإزالة أيقونة التحميل
	if _, err := bot.Request(tgbotapi.NewCallback(cq.ID, "")); err != nil {
		log.Printf("⚠️ فشل الرد على callback: %v", err)
	}

	switch cq.Data {
	case "my_packs":
		showMyPacks(chatID, userID)

	case "clone_pack":
		setState(userID, &UserState{
			ChatID: chatID,
			Step:   StepWaitingTitle,
		})
		sendForceReply(chatID, "الان ارسل اسم الحزمة الذي تريده 🗣")

	default:
		// تجاهل أي بيانات غير معروفة بأمان
	}
}

func showMyPacks(chatID, userID int64) {
	packsMu.RLock()
	list := append([]PackInfo(nil), userPacks[userID]...)
	packsMu.RUnlock()

	if len(list) == 0 {
		sendMainMenu(chatID, "📭 لا تملك أي حزمة حتى الآن.\n\nاضغط على \"🔄 نسخ حزمة ملصقات\" للبدء.")
		return
	}

	var sb strings.Builder
	sb.WriteString("📦 حزماتك:\n\n")
	for i, p := range list {
		sb.WriteString(fmt.Sprintf("%d. %s\nhttps://t.me/addstickers/%s\n\n", i+1, p.Title, p.Name))
	}
	sendMainMenu(chatID, sb.String())
}

// ==================== معالجة الرسائل ====================

func handleMessage(message *tgbotapi.Message) {
	chatID := message.Chat.ID
	userID := message.From.ID

	if message.IsCommand() && message.Command() == "start" {
		clearState(userID)
		sendWelcome(chatID)
		return
	}

	st := getState(userID)
	if st == nil || st.Step == StepNone {
		// لا يوجد سياق محادثة نشط لهذا المستخدم، نتجاهل الرسالة بهدوء
		return
	}

	switch st.Step {
	case StepWaitingTitle:
		handleTitleStep(message, st)
	case StepWaitingShortName:
		handleShortNameStep(message, st)
	case StepWaitingSticker:
		handleFirstStickerStep(message, st)
	case StepWaitingContinueSticker:
		handleContinueStickerStep(message, st)
	}
}

// ---- الخطوة 1: اسم الحزمة ----

func handleTitleStep(message *tgbotapi.Message, st *UserState) {
	title := strings.TrimSpace(message.Text)
	if title == "" {
		sendForceReply(st.ChatID, "⚠️ الرجاء إرسال اسم نصي صحيح للحزمة.\n\nالان ارسل اسم الحزمة الذي تريده 🗣")
		return
	}
	st.PackTitle = title
	st.Step = StepWaitingShortName
	setState(message.From.ID, st)
	sendForceReply(st.ChatID, "الان ارسل معرف الحزمة الذي تريده 🗣")
}

// ---- الخطوة 2: معرف الحزمة ----

func handleShortNameStep(message *tgbotapi.Message, st *UserState) {
	raw := strings.TrimSpace(message.Text)
	if raw == "" {
		sendForceReply(st.ChatID, "⚠️ الرجاء إرسال معرف صحيح (حروف/أرقام).\n\nالان ارسل معرف الحزمة الذي تريده 🗣")
		return
	}
	st.RawShortName = raw
	st.Step = StepWaitingSticker
	setState(message.From.ID, st)
	sendForceReply(st.ChatID, "ارسل ملصق من الحزمة التي تود نسخها 😃")
}

// ---- الخطوة 3: استقبال أول ملصق وبدء النسخ ----

func handleFirstStickerStep(message *tgbotapi.Message, st *UserState) {
	userID := message.From.ID

	if message.Sticker == nil {
		sendForceReply(st.ChatID, "⚠️ هذه ليست ملصقاً. الرجاء إرسال ملصق فعلي من الحزمة.\n\nارسل ملصق من الحزمة التي تود نسخها 😃")
		return
	}

	sourceSetName := message.Sticker.SetName
	if sourceSetName == "" {
		sendForceReply(st.ChatID, "⚠️ هذا الملصق لا ينتمي إلى أي حزمة. أرسل ملصقاً من داخل حزمة ملصقات حقيقية.\n\nارسل ملصق من الحزمة التي تود نسخها 😃")
		return
	}

	bot.Send(tgbotapi.NewMessage(st.ChatID, "⏳ جاري إنشاء الحزمة ونسخ أول دفعة من الملصقات، الرجاء الانتظار..."))

	set, err := bot.GetStickerSet(tgbotapi.GetStickerSetConfig{Name: sourceSetName})
	if err != nil || len(set.Stickers) == 0 {
		log.Printf("❌ فشل جلب الحزمة المصدر (%s): %v", sourceSetName, err)
		clearState(userID)
		sendMainMenu(st.ChatID, "❌ تعذّر قراءة حزمة الملصقات المصدر. حاول مرة أخرى من القائمة الرئيسية.")
		return
	}

	st.SourceSetName = sourceSetName
	st.SourceStickers = set.Stickers
	st.FinalShortName = buildFinalShortName(st.RawShortName)

	// إنشاء الحزمة الجديدة بأول ملصق
	if err := createNewStickerSet(userID, st.FinalShortName, st.PackTitle, set.Stickers[0]); err != nil {
		log.Printf("❌ فشل إنشاء الحزمة الجديدة: %v", err)
		// محاولة ثانية بمعرف عشوائي مختلف تلافياً لأي تعارض نادر بالاسم
		st.FinalShortName = buildFinalShortName(st.RawShortName)
		if err2 := createNewStickerSet(userID, st.FinalShortName, st.PackTitle, set.Stickers[0]); err2 != nil {
			log.Printf("❌ فشل المحاولة الثانية أيضاً: %v", err2)
			clearState(userID)
			sendMainMenu(st.ChatID, "❌ تعذّر إنشاء حزمة الملصقات. تأكد أن البوت يملك صلاحية إدارة الملصقات وحاول مجدداً.")
			return
		}
	}

	copied := 1
	limit := firstBatchSize
	if limit > len(set.Stickers) {
		limit = len(set.Stickers)
	}

	for i := 1; i < limit; i++ {
		if err := addStickerToSet(userID, st.FinalShortName, set.Stickers[i]); err != nil {
			log.Printf("⚠️ فشل نسخ الملصق رقم %d: %v", i, err)
			continue
		}
		copied++
		time.Sleep(150 * time.Millisecond) // تفادي حدود الإرسال المتكرر لدى تيليجرام
	}

	st.NextIndex = limit
	setState(userID, st)

	link := fmt.Sprintf("https://t.me/addstickers/%s", st.FinalShortName)

	if st.NextIndex >= len(set.Stickers) {
		// اكتملت كل الملصقات ضمن الدفعة الأولى
		finishClone(userID, st)
		return
	}

	st.Step = StepWaitingContinueSticker
	setState(userID, st)

	text := fmt.Sprintf(
		"✅ تم إنشاء الحزمة بنجاح!\n\n📦 الاسم: %s\n🔗 الرابط: %s\n✔️ تم نسخ %d من أصل %d ملصق\n\n- اعد ارسال الملصق لاكمال نسخ بقية الملصقات .",
		st.PackTitle, link, copied, len(set.Stickers),
	)
	sendForceReply(st.ChatID, text)
}

// ---- الخطوة 4: إعادة إرسال الملصق لإكمال باقي الحزمة ----

func handleContinueStickerStep(message *tgbotapi.Message, st *UserState) {
	userID := message.From.ID

	if message.Sticker == nil {
		sendForceReply(st.ChatID, "⚠️ الرجاء إعادة إرسال نفس الملصق لإكمال النسخ.\n\n- اعد ارسال الملصق لاكمال نسخ بقية الملصقات .")
		return
	}

	bot.Send(tgbotapi.NewMessage(st.ChatID, "⏳ جاري نسخ باقي الملصقات..."))

	remaining := st.SourceStickers[st.NextIndex:]
	copiedNow := 0
	for _, s := range remaining {
		if err := addStickerToSet(userID, st.FinalShortName, s); err != nil {
			log.Printf("⚠️ فشل نسخ ملصق أثناء الدفعة الثانية: %v", err)
			continue
		}
		copiedNow++
		time.Sleep(150 * time.Millisecond)
	}

	st.NextIndex = len(st.SourceStickers)
	setState(userID, st)

	finishClone(userID, st)
}

// ---- إنهاء عملية النسخ ----

func finishClone(userID int64, st *UserState) {
	packsMu.Lock()
	userPacks[userID] = append(userPacks[userID], PackInfo{Title: st.PackTitle, Name: st.FinalShortName})
	packsMu.Unlock()

	chatID := st.ChatID
	total := len(st.SourceStickers)
	link := fmt.Sprintf("https://t.me/addstickers/%s", st.FinalShortName)

	clearState(userID)

	text := fmt.Sprintf(
		"🎉 تم نسخ الحزمة بالكامل بنجاح!\n\n📦 الاسم: %s\n🔗 الرابط: %s\n✅ عدد الملصقات: %d\n\nيمكنك متابعة حزماتك من القائمة الرئيسية.",
		st.PackTitle, link, total,
	)
	sendMainMenu(chatID, text)
}

// ==================== دوال مساعدة لحالة المستخدم ====================

func getState(userID int64) *UserState {
	statesMu.RLock()
	defer statesMu.RUnlock()
	return states[userID]
}

func setState(userID int64, st *UserState) {
	statesMu.Lock()
	defer statesMu.Unlock()
	states[userID] = st
}

func clearState(userID int64) {
	statesMu.Lock()
	defer statesMu.Unlock()
	delete(states, userID)
}

// ==================== دوال مساعدة لإرسال الرسائل ====================

func sendForceReply(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ReplyMarkup = tgbotapi.ForceReply{ForceReply: true, Selective: false}
	if _, err := bot.Send(msg); err != nil {
		log.Printf("❌ فشل إرسال رسالة ForceReply: %v", err)
	}
}

// ==================== دوال مساعدة لحزم الملصقات ====================

func emojiOrDefault(e string) string {
	if strings.TrimSpace(e) == "" {
		return "😀"
	}
	return e
}

func createNewStickerSet(userID int64, name, title string, sticker tgbotapi.Sticker) error {
	cfg := tgbotapi.CreateNewStickerSetConfig{
		UserID: userID,
		Name:   name,
		Title:  title,
		Emojis: emojiOrDefault(sticker.Emoji),
	}
	switch {
	case sticker.IsAnimated:
		cfg.TGSSticker = tgbotapi.FileID(sticker.FileID)
	case sticker.IsVideo:
		cfg.WebmSticker = tgbotapi.FileID(sticker.FileID)
	default:
		cfg.PNGSticker = tgbotapi.FileID(sticker.FileID)
	}
	_, err := bot.Request(cfg)
	return err
}

func addStickerToSet(userID int64, name string, sticker tgbotapi.Sticker) error {
	cfg := tgbotapi.AddStickerToSetConfig{
		UserID: userID,
		Name:   name,
		Emojis: emojiOrDefault(sticker.Emoji),
	}
	switch {
	case sticker.IsAnimated:
		cfg.TGSSticker = tgbotapi.FileID(sticker.FileID)
	case sticker.IsVideo:
		cfg.WebmSticker = tgbotapi.FileID(sticker.FileID)
	default:
		cfg.PNGSticker = tgbotapi.FileID(sticker.FileID)
	}
	_, err := bot.Request(cfg)
	return err
}

// ==================== توليد معرف فريد وآمن للحزمة ====================

// sanitizeShortName ينظّف مدخل المستخدم بحيث يطابق شروط تيليجرام:
// حروف إنجليزية وأرقام وشرطة سفلية فقط، ويبدأ بحرف، وبدون شرطات متتالية.
func sanitizeShortName(input string) string {
	var b strings.Builder
	for _, r := range input {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		}
	}
	s := b.String()
	if s == "" {
		s = "pack"
	}
	if !unicode.IsLetter(rune(s[0])) {
		s = "s" + s
	}
	for strings.Contains(s, "__") {
		s = strings.ReplaceAll(s, "__", "_")
	}
	if len(s) > 25 {
		s = s[:25]
	}
	return s
}

func randomSuffix(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

// buildFinalShortName يبني معرفاً فريداً كاملاً بالصيغة المطلوبة من تيليجرام:
// <اسم منظف>_<عشوائي>_by_<اسم البوت>
func buildFinalShortName(rawInput string) string {
	prefix := sanitizeShortName(rawInput)
	suffix := "_" + randomSuffix(6) + "_by_" + bot.Self.UserName

	maxTotal := 64
	if len(prefix)+len(suffix) > maxTotal {
		allowed := maxTotal - len(suffix)
		if allowed < 1 {
			allowed = 1
		}
		if allowed < len(prefix) {
			prefix = prefix[:allowed]
		}
	}
	return prefix + suffix
}
