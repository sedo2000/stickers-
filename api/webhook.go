// Package handler يحتوي على Webhook الخاص ببوت تيليجرام لاستنساخ حزم الملصقات.
// يعمل كدالة Serverless على منصة Vercel (Go Runtime).
package handler

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// =============================================================================
// إعداد البوت (Bot Initialization)
// =============================================================================

var (
	bot        *tgbotapi.BotAPI
	botOnce    sync.Once
	botInitErr error

	// عميل HTTP مخصص لاستدعاء Telegram API مباشرة (لإنشاء/إضافة الملصقات)
	httpClient = &http.Client{Timeout: 30 * time.Second}

	botUsername = "Cut88bot" // يجب أن يطابق يوزر البوت الفعلي، مستخدم في تكوين اسم الحزمة
)

// initBot يقوم بتهيئة اتصال البوت مرة واحدة فقط (Singleton) عبر جميع الاستدعاءات
func initBot() {
	botOnce.Do(func() {
		token := os.Getenv("BOT_TOKEN")
		if token == "" {
			botInitErr = fmt.Errorf("متغير البيئة BOT_TOKEN غير موجود")
			log.Println(botInitErr)
			return
		}
		b, err := tgbotapi.NewBotAPI(token)
		if err != nil {
			botInitErr = err
			log.Printf("فشل تهيئة البوت: %v", err)
			return
		}
		bot = b
	})
}

// =============================================================================
// إدارة الجلسات (Session Management) - في الذاكرة، آمنة للتزامن (Thread-Safe)
// =============================================================================

type PackType string

const (
	PackStatic   PackType = "static"   // ملصقات ثابتة PNG/WEBP
	PackAnimated PackType = "animated" // ملصقات متحركة TGS
	PackVideo    PackType = "video"    // ملصقات فيديو WEBM
)

type SessionStep string

const (
	StepNone            SessionStep = ""
	StepAwaitingForward SessionStep = "awaiting_forward" // ننتظر أن يفوّرد المستخدم ملصق من الحزمة الأصلية
	StepAwaitingTitle   SessionStep = "awaiting_title"   // ننتظر أن يرسل المستخدم عنوان الحزمة الجديدة
)

// UserSession يمثل حالة المستخدم الحالية أثناء عملية النسخ
type UserSession struct {
	ChatID         int64
	OwnerID        int64 // معرف المستخدم صاحب الحزمة الجديدة (مطلوب لـ createNewStickerSet)
	Step           SessionStep
	PackType       PackType
	SourceStickers []tgbotapi.Sticker
	NewPackTitle   string
	NewPackName    string
}

var (
	sessions   = make(map[int64]*UserSession)
	sessionsMu sync.Mutex
)

func getSession(chatID int64) *UserSession {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	return sessions[chatID]
}

func setSession(chatID int64, s *UserSession) {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	sessions[chatID] = s
}

func clearSession(chatID int64) {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	delete(sessions, chatID)
}

// =============================================================================
// نقطة الدخول الرئيسية (Vercel Entry Point)
// =============================================================================

// Handler هي الدالة التي تستدعيها Vercel عند وصول طلب إلى /api/webhook
func Handler(w http.ResponseWriter, r *http.Request) {
	initBot()

	// نرد بـ 200 دائماً بسرعة حتى لا يعيد تيليجرام إرسال نفس التحديث
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("panic تم اعتراضه: %v", rec)
		}
	}()

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusOK)
		return
	}
	defer r.Body.Close()

	if botInitErr != nil {
		log.Printf("البوت غير مهيأ: %v", botInitErr)
		w.WriteHeader(http.StatusOK)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	var update tgbotapi.Update
	if err := json.Unmarshal(body, &update); err != nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	// المعالجة تتم بشكل متزامن (Synchronous) قبل الرد،
	// لأن دوال Vercel الخلفية (goroutines) قد يتم تجميدها بعد إرسال الرد.
	processUpdate(update)

	w.WriteHeader(http.StatusOK)
}

func processUpdate(update tgbotapi.Update) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("panic أثناء معالجة التحديث: %v", rec)
		}
	}()

	switch {
	case update.CallbackQuery != nil:
		handleCallback(update.CallbackQuery)
	case update.Message != nil:
		handleMessage(update.Message)
	}
}

// =============================================================================
// القائمة الرئيسية والتنقل
// =============================================================================

func sendMainMenu(chatID int64) {
	text := "👋 أهلاً بك في بوت نسخ الملصقات!\n\nاختر الخدمة التي تريدها:"
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🖼 نسخ حزمة ملصقات ثابتة", "copy_static"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🎞 نسخ حزمة ملصقات متحركة", "copy_animated"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📁 حزماتي (إدارة الحزم)", "my_packs"),
		),
	)
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ReplyMarkup = keyboard
	if _, err := bot.Send(msg); err != nil {
		log.Printf("خطأ في إرسال القائمة الرئيسية: %v", err)
	}
}

func sendMyPacksMenu(chatID int64) {
	text := "📁 إدارة الحزمات الخاصة بك:"
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("✏️ تعديل عنوان حزمة", "edit_title_instructions"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🗑 حذف ملصق من حزمة", "delete_sticker_instructions"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🔙 رجوع للقائمة الرئيسية", "back_main"),
		),
	)
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ReplyMarkup = keyboard
	bot.Send(msg)
}

// =============================================================================
// معالجة أزرار الـ Inline Keyboard (Callback Queries)
// =============================================================================

func handleCallback(cb *tgbotapi.CallbackQuery) {
	chatID := cb.Message.Chat.ID

	// إجباري: الرد على الـ callback حتى تختفي دائرة التحميل من الزر
	bot.Request(tgbotapi.NewCallback(cb.ID, ""))

	switch cb.Data {

	case "noop":
		// زر شريط التقدم - غير قابل للنقر فعلياً، فقط للعرض
		return

	case "copy_static":
		setSession(chatID, &UserSession{
			ChatID:   chatID,
			OwnerID:  cb.From.ID,
			Step:     StepAwaitingForward,
			PackType: PackStatic,
		})
		bot.Send(tgbotapi.NewMessage(chatID,
			"📥 أرسل (فوّرد) أي ملصق واحد من الحزمة *الثابتة* التي تريد نسخها."))

	case "copy_animated":
		setSession(chatID, &UserSession{
			ChatID:   chatID,
			OwnerID:  cb.From.ID,
			Step:     StepAwaitingForward,
			PackType: PackAnimated, // سيتم تصحيحه تلقائياً إلى Video إن لزم بعد استلام الملصق
		})
		bot.Send(tgbotapi.NewMessage(chatID,
			"📥 أرسل (فوّرد) أي ملصق واحد من الحزمة *المتحركة* (متحركة أو فيديو) التي تريد نسخها."))

	case "my_packs":
		sendMyPacksMenu(chatID)

	case "edit_title_instructions":
		bot.Send(tgbotapi.NewMessage(chatID,
			"✏️ لتعديل عنوان حزمة ملصقات:\n\n"+
				"1) توجه إلى المحادثة مع @Stickers الرسمي.\n"+
				"2) أرسل الأمر /setstickersettitle\n"+
				"3) أرسل اسم الحزمة (name) الخاص بحزمتك.\n"+
				"4) أرسل العنوان الجديد.\n\n"+
				"⚠️ يجب أن تكون أنت منشئ الحزمة الأصلي."))

	case "delete_sticker_instructions":
		bot.Send(tgbotapi.NewMessage(chatID,
			"🗑 لحذف ملصق من حزمة:\n\n"+
				"1) توجه إلى المحادثة مع @Stickers الرسمي.\n"+
				"2) أرسل الأمر /delsticker\n"+
				"3) فوّرد الملصق الذي تريد حذفه من الحزمة.\n\n"+
				"⚠️ يجب أن تكون أنت منشئ الحزمة الأصلي."))

	case "back_main":
		clearSession(chatID)
		sendMainMenu(chatID)
	}
}

// =============================================================================
// معالجة الرسائل النصية والملصقات المُفوّردة
// =============================================================================

func handleMessage(msg *tgbotapi.Message) {
	chatID := msg.Chat.ID

	if msg.Text == "/start" {
		clearSession(chatID)
		sendMainMenu(chatID)
		return
	}

	session := getSession(chatID)
	if session == nil {
		// لا توجد عملية جارية لهذا المستخدم، نتجاهل الرسالة
		return
	}

	switch session.Step {
	case StepAwaitingForward:
		handleForwardedSticker(session, msg)
	case StepAwaitingTitle:
		handleTitleInput(session, msg)
	}
}

// handleForwardedSticker يتحقق من الملصق المُفوّرد ويجلب بيانات الحزمة الأصلية كاملة
func handleForwardedSticker(session *UserSession, msg *tgbotapi.Message) {
	chatID := msg.Chat.ID

	if msg.Sticker == nil || msg.Sticker.SetName == "" {
		bot.Send(tgbotapi.NewMessage(chatID,
			"⚠️ الرجاء إرسال (فوّرد) ملصق واحد فعلي من الحزمة الأصلية، وليس نصاً أو صورة."))
		return
	}

	sticker := msg.Sticker
	isStatic := !sticker.IsAnimated && !sticker.IsVideo

	// --- التحقق من تطابق النوع مع القسم الذي اختاره المستخدم ---
	if session.PackType == PackStatic && !isStatic {
		bot.Send(tgbotapi.NewMessage(chatID,
			"❌ هذه حزمة متحركة أو فيديو وليست ثابتة!\nالرجاء استخدام قسم «🎞 نسخ حزمة ملصقات متحركة» بدلاً من ذلك."))
		return
	}
	if session.PackType == PackAnimated && isStatic {
		bot.Send(tgbotapi.NewMessage(chatID,
			"❌ هذه حزمة ثابتة وليست متحركة!\nالرجاء استخدام قسم «🖼 نسخ حزمة ملصقات ثابتة» بدلاً من ذلك."))
		return
	}

	// تحديد النوع الدقيق: متحركة TGS أو فيديو WEBM (كلاهما يدخلان من زر "متحركة")
	actualType := PackStatic
	switch {
	case sticker.IsAnimated:
		actualType = PackAnimated
	case sticker.IsVideo:
		actualType = PackVideo
	}

	loadingMsg, _ := bot.Send(tgbotapi.NewMessage(chatID, "🔍 جاري جلب بيانات الحزمة الأصلية..."))

	stickerSet, err := getStickerSet(sticker.SetName)
	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ تعذر جلب الحزمة الأصلية، تأكد من الملصق وحاول مرة أخرى."))
		return
	}
	if len(stickerSet.Stickers) == 0 {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ الحزمة الأصلية فارغة!"))
		return
	}

	session.PackType = actualType
	session.SourceStickers = stickerSet.Stickers
	session.Step = StepAwaitingTitle
	setSession(chatID, session)

	bot.Request(tgbotapi.NewDeleteMessage(chatID, loadingMsg.MessageID))

	bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf(
		"✅ تم العثور على %d ملصق في الحزمة الأصلية.\n\n✏️ الآن أرسل العنوان الذي تريده للحزمة الجديدة:",
		len(stickerSet.Stickers))))
}

// handleTitleInput يستقبل العنوان، يضيف العلامة المائية، وينشئ اسم الحزمة، ثم يبدأ الاستنساخ
func handleTitleInput(session *UserSession, msg *tgbotapi.Message) {
	chatID := msg.Chat.ID
	title := strings.TrimSpace(msg.Text)
	if title == "" {
		bot.Send(tgbotapi.NewMessage(chatID, "⚠️ الرجاء إرسال عنوان نصي صالح."))
		return
	}

	const watermark = " | @I5I5Ie"
	const maxTitleLen = 64 // الحد الأقصى المسموح به من تيليجرام لعنوان الحزمة

	finalTitle := title + watermark
	if len(finalTitle) > maxTitleLen {
		// نقص العنوان الأصلي حتى يبقى مجال للعلامة المائية بدون تجاوز الحد
		allowed := maxTitleLen - len(watermark)
		if allowed < 0 {
			allowed = 0
		}
		if len(title) > allowed {
			title = title[:allowed]
		}
		finalTitle = title + watermark
	}

	session.NewPackTitle = finalTitle
	session.NewPackName = generatePackName()
	session.Step = StepNone
	setSession(chatID, session)

	startCloning(session, chatID)
}

// generatePackName ينشئ اسماً فريداً للحزمة بالصيغة: p<أرقام عشوائية>_by_Cut88bot
func generatePackName() string {
	n, err := rand.Int(rand.Reader, big.NewInt(90000000))
	num := int64(10000000)
	if err == nil {
		num += n.Int64()
	} else {
		// احتياطي في حال فشل التوليد العشوائي الآمن
		num += time.Now().UnixNano() % 90000000
	}
	return fmt.Sprintf("p%d_by_%s", num, botUsername)
}

// =============================================================================
// عملية الاستنساخ الفعلية + شريط التقدم التفاعلي
// =============================================================================

func startCloning(session *UserSession, chatID int64) {
	total := len(session.SourceStickers)

	progressMsg := tgbotapi.NewMessage(chatID, "🚀 جاري معالجة ورفع الحزمة، الرجاء الانتظار...")
	progressMsg.ReplyMarkup = progressKeyboard(0, total)
	sentMsg, err := bot.Send(progressMsg)
	if err != nil {
		log.Printf("فشل إرسال رسالة التقدم: %v", err)
		return
	}

	for i, sticker := range session.SourceStickers {
		emoji := sticker.Emoji
		if emoji == "" {
			emoji = "🙂"
		}

		if i == 0 {
			err = apiCreateNewStickerSet(session, sticker.FileID, emoji)
		} else {
			err = apiAddStickerToSet(session, sticker.FileID, emoji)
		}

		if err != nil {
			log.Printf("فشل إضافة الملصق رقم %d: %v", i+1, err)
			bot.Request(tgbotapi.NewDeleteMessage(chatID, sentMsg.MessageID))
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf(
				"⚠️ توقفت العملية عند الملصق رقم %d بسبب خطأ:\n%v", i+1, err)))
			clearSession(chatID)
			sendMainMenu(chatID)
			return
		}

		// تحديث شريط التقدم فوراً بعد كل ملصق (بدون أي تأخير/Sleep)
		edit := tgbotapi.NewEditMessageReplyMarkup(chatID, sentMsg.MessageID, progressKeyboard(i+1, total))
		bot.Request(edit)
	}

	// اكتملت النسبة 100% -> نحذف رسالة شريط التقدم فوراً
	bot.Request(tgbotapi.NewDeleteMessage(chatID, sentMsg.MessageID))

	link := fmt.Sprintf("https://t.me/addstickers/%s", session.NewPackName)
	successText := fmt.Sprintf(
		"✅ تم إنشاء الحزمة بنجاح!\n\n📌 العنوان: %s\n🔗 الرابط: %s",
		session.NewPackTitle, link)
	bot.Send(tgbotapi.NewMessage(chatID, successText))

	clearSession(chatID)
	sendMainMenu(chatID)
}

// progressKeyboard يبني زر Inline واحد يعمل كشريط تقدم بصري
func progressKeyboard(done, total int) tgbotapi.InlineKeyboardMarkup {
	percent := 0
	if total > 0 {
		percent = (done * 100) / total
	}
	const barLength = 10
	filled := (percent * barLength) / 100
	if filled > barLength {
		filled = barLength
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", barLength-filled)

	label := fmt.Sprintf("⏳ [%s] %d%% (%d/%d)", bar, percent, done, total)
	if done >= total && total > 0 {
		label = fmt.Sprintf("✅ [%s] 100%% (%d/%d)", bar, total, total)
	}

	btn := tgbotapi.NewInlineKeyboardButtonData(label, "noop")
	return tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(btn))
}

// =============================================================================
// استدعاءات Telegram Bot API الخام (Raw HTTP) لإنشاء/إضافة الملصقات
//
// ملاحظة مهمة: في الإصدارات الحديثة من Telegram Bot API (6.6+) تم استبدال
// الحقول القديمة (png_sticker / tgs_sticker / video_sticker) بحقل موحّد
// "stickers" (أو "sticker" عند الإضافة) من نوع InputSticker يحتوي على:
//   { "sticker": "<file_id>", "format": "static|animated|video", "emoji_list": [...] }
// استخدام الحقول القديمة على الإصدارات الحديثة هو السبب الشائع الآن لخطأ
// "Bad Request: there is no sticker file in the request". لذلك تم اعتماد
// البنية الصحيحة والحديثة أدناه لضمان عدم حدوث الخطأ 400.
// كما أننا نستخدم file_id مباشرة (بدون تحميل/رفع البايتات) لأقصى سرعة ممكنة.
// =============================================================================

type inputSticker struct {
	Sticker   string   `json:"sticker"`
	Format    string   `json:"format"`
	EmojiList []string `json:"emoji_list"`
}

type createStickerSetPayload struct {
	UserID   int64          `json:"user_id"`
	Name     string         `json:"name"`
	Title    string         `json:"title"`
	Stickers []inputSticker `json:"stickers"`
}

type addStickerPayload struct {
	UserID  int64        `json:"user_id"`
	Name    string       `json:"name"`
	Sticker inputSticker `json:"sticker"`
}

// packFormat يحوّل PackType الداخلي إلى القيمة النصية التي يتوقعها Telegram
func packFormat(t PackType) string {
	switch t {
	case PackAnimated:
		return "animated"
	case PackVideo:
		return "video"
	default:
		return "static"
	}
}

func apiCreateNewStickerSet(session *UserSession, fileID, emoji string) error {
	payload := createStickerSetPayload{
		UserID: session.OwnerID,
		Name:   session.NewPackName,
		Title:  session.NewPackTitle,
		Stickers: []inputSticker{
			{
				Sticker:   fileID,
				Format:    packFormat(session.PackType),
				EmojiList: []string{emoji},
			},
		},
	}
	_, err := callTelegramAPI("createNewStickerSet", payload)
	return err
}

func apiAddStickerToSet(session *UserSession, fileID, emoji string) error {
	payload := addStickerPayload{
		UserID: session.OwnerID,
		Name:   session.NewPackName,
		Sticker: inputSticker{
			Sticker:   fileID,
			Format:    packFormat(session.PackType),
			EmojiList: []string{emoji},
		},
	}
	_, err := callTelegramAPI("addStickerToSet", payload)
	return err
}

// getStickerSet يجلب كامل بيانات وملصقات الحزمة الأصلية عبر المكتبة الرسمية
func getStickerSet(name string) (*tgbotapi.StickerSet, error) {
	config := tgbotapi.GetStickerSetConfig{Name: name}
	set, err := bot.GetStickerSet(config)
	if err != nil {
		return nil, err
	}
	return &set, nil
}

// callTelegramAPI دالة عامة لاستدعاء أي Method من Telegram Bot API عبر JSON خام
func callTelegramAPI(method string, payload interface{}) ([]byte, error) {
	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("BOT_TOKEN غير معرّف")
	}

	url := fmt.Sprintf("https://api.telegram.org/bot%s/%s", token, method)
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var apiResp struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("فشل تحليل رد Telegram: %w (raw: %s)", err, string(respBody))
	}
	if !apiResp.OK {
		return nil, fmt.Errorf("خطأ من Telegram API: %s", apiResp.Description)
	}

	return respBody, nil
}
