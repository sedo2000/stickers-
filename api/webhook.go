
// api/webhook.go
// Webhook handler for Telegram bot hosted on Vercel.
// يتعامل مع طلبات الويب هوك ويدير جلسات المستخدمين لعملية نسخ حزم الملصقات.

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// ==================== تعريفات الحالة والجلسة ====================

// UserState يخزن حالة المستخدم أثناء التفاعل مع البوت.
type UserState struct {
	Action      string // "awaiting_title", "awaiting_sticker", "cloning"
	PackType    string // "static", "animated", "video"
	Title       string
	PackName    string
	StickerSet  *tgbotapi.StickerSet
	Stickers    []tgbotapi.Sticker // قائمة الملصقات المسترجعة
	Index       int                // الفهرس الحالي أثناء الرفع
	ProgressMsg struct {
		ChatID int64
		MsgID  int
	}
}

// المتغيرات العامة مع أقفال للتعامل المتزامن.
var (
	bot      *tgbotapi.BotAPI
	sessions = make(map[int64]*UserState)
	mu       sync.RWMutex
)

// ==================== الدوال المساعدة ====================

// randInt يولد عددًا عشوائيًا بين min و max.
func randInt(min, max int) int {
	return rand.Intn(max-min) + min
}

// generatePackName يولد اسم حزمة جديد بالصيغة المطلوبة.
func generatePackName() string {
	rand.Seed(time.Now().UnixNano())
	num := randInt(100000, 999999)
	return fmt.Sprintf("p%d_by_Cut88bot", num)
}

// getStickerSetByName يسترجع مجموعة الملصقات باستخدام الاسم.
func getStickerSetByName(name string) (*tgbotapi.StickerSet, error) {
	cfg := tgbotapi.GetStickerSetConfig{
		Name: name,
	}
	return bot.GetStickerSet(cfg)
}

// createNewStickerSet ينشئ حزمة جديدة بالبيانات المحددة.
func createNewStickerSet(userID int64, name, title, packType, fileID, emoji string) error {
	cfg := tgbotapi.CreateNewStickerSetConfig{
		UserID: userID,
		Name:   name,
		Title:  title,
		Emojis: emoji,
	}

	switch packType {
	case "static":
		cfg.PNGSticker = fileID
	case "animated":
		cfg.TGSSticker = fileID
	case "video":
		cfg.WebMSticker = fileID
	default:
		return fmt.Errorf("نوع حزمة غير معروف: %s", packType)
	}

	_, err := bot.CreateNewStickerSet(cfg)
	return err
}

// addStickerToSet يضيف ملصقًا إلى حزمة موجودة.
func addStickerToSet(userID int64, name, packType, fileID, emoji string) error {
	cfg := tgbotapi.AddStickerToSetConfig{
		UserID: userID,
		Name:   name,
		Emojis: emoji,
	}

	switch packType {
	case "static":
		cfg.PNGSticker = fileID
	case "animated":
		cfg.TGSSticker = fileID
	case "video":
		cfg.WebMSticker = fileID
	default:
		return fmt.Errorf("نوع حزمة غير معروف: %s", packType)
	}

	_, err := bot.AddStickerToSet(cfg)
	return err
}

// ==================== دوال التعامل مع الرسائل والاستعلامات ====================

// sendMainMenu يرسل القائمة الرئيسية للمستخدم.
func sendMainMenu(chatID int64) error {
	msg := tgbotapi.NewMessage(chatID, "مرحباً بك في بوت نسخ الملصقات! اختر نوع الحزمة التي تريد نسخها:")
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(
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
	_, err := bot.Send(msg)
	return err
}

// handleStart يعالج الأمر /start.
func handleStart(update tgbotapi.Update) {
	if update.Message == nil {
		return
	}
	chatID := update.Message.Chat.ID
	// تنظيف أي جلسة سابقة
	mu.Lock()
	delete(sessions, chatID)
	mu.Unlock()

	sendMainMenu(chatID)
}

// handleCallbackQuery يعالج النقرات على الأزرار.
func handleCallbackQuery(update tgbotapi.Update) {
	cb := update.CallbackQuery
	if cb == nil {
		return
	}
	chatID := cb.Message.Chat.ID
	data := cb.Data

	// الرد على الاستعلام لتجنب انتظار التحميل.
	bot.Send(tgbotapi.NewCallback(cb.ID, ""))

	switch data {
	case "copy_static":
		// تعيين نوع الحزمة وطلب العنوان
		mu.Lock()
		sessions[chatID] = &UserState{
			Action:   "awaiting_title",
			PackType: "static",
		}
		mu.Unlock()
		msg := tgbotapi.NewMessage(chatID, "أرسل العنوان المخصص للحزمة الجديدة (سيتم إضافة العلامة المائية تلقائياً).")
		bot.Send(msg)

	case "copy_animated":
		// نطلب عنوانًا، لكن سنحدد نوع الحزمة لاحقًا بناءً على الملصق المرسل؟
		// لكن المستخدم اختار متحركة، لذا سنطلب عنوانًا وننتظر ملصقًا متحركًا.
		mu.Lock()
		sessions[chatID] = &UserState{
			Action:   "awaiting_title",
			PackType: "animated", // سيتم التحقق لاحقًا
		}
		mu.Unlock()
		msg := tgbotapi.NewMessage(chatID, "أرسل العنوان المخصص للحزمة المتحركة (سيتم إضافة العلامة المائية تلقائياً).")
		bot.Send(msg)

	case "my_packs":
		// عرض قائمة إدارة الحزم (تعليمات فقط)
		menu := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("✏️ تعديل عنوان حزمة", "edit_title"),
			),
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("🗑 حذف ملصق من حزمة", "delete_sticker"),
			),
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("🔙 رجوع للقائمة الرئيسية", "main_menu"),
			),
		)
		msg := tgbotapi.NewMessage(chatID, "اختر العملية المطلوبة:")
		msg.ReplyMarkup = menu
		bot.Send(msg)

	case "edit_title", "delete_sticker":
		// إرسال تعليمات نصية بسيطة.
		var text string
		if data == "edit_title" {
			text = "لتعديل عنوان حزمة، استخدم الأمر /editpack <اسم_الحزمة> <العنوان_الجديد> (هذه الميزة قيد التطوير)."
		} else {
			text = "لحذف ملصق من حزمة، استخدم الأمر /delsticker <اسم_الحزمة> <معرف_الملصق> (هذه الميزة قيد التطوير)."
		}
		msg := tgbotapi.NewMessage(chatID, text)
		bot.Send(msg)

	case "main_menu":
		sendMainMenu(chatID)

	default:
		// تجاهل
	}
}

// handleMessage يعالج الرسائل النصية والملصقات.
func handleMessage(update tgbotapi.Update) {
	if update.Message == nil {
		return
	}
	chatID := update.Message.Chat.ID

	mu.RLock()
	state, exists := sessions[chatID]
	mu.RUnlock()

	if !exists {
		// إذا لم توجد جلسة، نرسل القائمة الرئيسية.
		sendMainMenu(chatID)
		return
	}

	// معالجة حسب الحالة
	switch state.Action {
	case "awaiting_title":
		// ننتظر إرسال عنوان
		if update.Message.Text == "" {
			bot.Send(tgbotapi.NewMessage(chatID, "الرجاء إرسال نص العنوان."))
			return
		}
		title := strings.TrimSpace(update.Message.Text)
		if title == "" {
			bot.Send(tgbotapi.NewMessage(chatID, "العنوان لا يمكن أن يكون فارغاً."))
			return
		}
		// إضافة العلامة المائية
		fullTitle := title + " | @I5I5Ie"
		// حفظ العنوان وانتظار إرسال ملصق
		mu.Lock()
		state.Title = fullTitle
		state.Action = "awaiting_sticker"
		mu.Unlock()

		msg := tgbotapi.NewMessage(chatID, "تم حفظ العنوان. الآن أرسل أي ملصق من الحزمة التي تريد نسخها (يمكنك إعادة توجيهه).")
		bot.Send(msg)

	case "awaiting_sticker":
		// ننتظر ملصقًا
		if update.Message.Sticker == nil {
			bot.Send(tgbotapi.NewMessage(chatID, "الرجاء إرسال ملصق (Sticker) من الحزمة المطلوبة."))
			return
		}
		sticker := update.Message.Sticker
		setName := sticker.SetName
		if setName == "" {
			bot.Send(tgbotapi.NewMessage(chatID, "هذا الملصق ليس جزءًا من حزمة، الرجاء إرسال ملصق من حزمة صالحة."))
			return
		}

		// التحقق من تطابق نوع الملصق مع النوع المطلوب
		expectedType := state.PackType
		var actualType string
		if sticker.IsVideo {
			actualType = "video"
		} else if sticker.IsAnimated {
			actualType = "animated"
		} else {
			actualType = "static"
		}

		if expectedType != actualType {
			msg := fmt.Sprintf("لقد اخترت نسخ حزمة %s، لكن الملصق المرسل من نوع %s. الرجاء استخدام القسم المناسب.", expectedType, actualType)
			bot.Send(tgbotapi.NewMessage(chatID, msg))
			return
		}

		// جلب الحزمة الكاملة
		set, err := getStickerSetByName(setName)
		if err != nil {
			log.Printf("خطأ في جلب الحزمة %s: %v", setName, err)
			bot.Send(tgbotapi.NewMessage(chatID, "حدث خطأ أثناء جلب الحزمة. تأكد من أن الحزمة عامة وحاول مرة أخرى."))
			return
		}

		// تخزين بيانات الحزمة
		mu.Lock()
		state.StickerSet = set
		state.Stickers = set.Stickers
		state.Index = 0
		state.Action = "cloning"
		mu.Unlock()

		// بدء عملية النسخ
		go clonePack(chatID)

	case "cloning":
		// أثناء النسخ، نرفض أي رسائل أخرى
		bot.Send(tgbotapi.NewMessage(chatID, "الرجاء الانتظار حتى انتهاء عملية النسخ الحالية."))

	default:
		// حالة غير معروفة
		mu.Lock()
		delete(sessions, chatID)
		mu.Unlock()
		sendMainMenu(chatID)
	}
}

// ==================== عملية النسخ ====================

// clonePack تقوم بعملية النسخ الفعلية مع تحديث شريط التقدم.
func clonePack(chatID int64) {
	mu.RLock()
	state, exists := sessions[chatID]
	mu.RUnlock()
	if !exists || state.Action != "cloning" {
		return
	}

	userID := chatID
	packName := generatePackName()
	fullTitle := state.Title
	packType := state.PackType
	stickers := state.Stickers
	total := len(stickers)

	// إنشاء الحزمة بالملصق الأول
	firstSticker := stickers[0]
	emoji := firstSticker.Emoji
	if emoji == "" {
		emoji = "🤖" // إيموجي افتراضي
	}
	err := createNewStickerSet(userID, packName, fullTitle, packType, firstSticker.FileID, emoji)
	if err != nil {
		log.Printf("فشل إنشاء الحزمة: %v", err)
		bot.Send(tgbotapi.NewMessage(chatID, "حدث خطأ أثناء إنشاء الحزمة: "+err.Error()))
		mu.Lock()
		delete(sessions, chatID)
		mu.Unlock()
		return
	}

	// إرسال رسالة التقدم الأولى
	progressMsg := tgbotapi.NewMessage(chatID, "جاري معالجة ورفع الحزمة...")
	btnText := fmt.Sprintf("⏳ [░░░░░░░░░░] 0%% (0/%d)", total)
	btn := tgbotapi.NewInlineKeyboardButtonData(btnText, "progress_dummy")
	row := tgbotapi.NewInlineKeyboardRow(btn)
	markup := tgbotapi.NewInlineKeyboardMarkup(row)
	progressMsg.ReplyMarkup = markup
	sentMsg, err := bot.Send(progressMsg)
	if err != nil {
		log.Printf("فشل إرسال رسالة التقدم: %v", err)
		// استمرار دون شريط تقدم
	} else {
		// تخزين معرف الرسالة لتحديثها
		mu.Lock()
		state.ProgressMsg.ChatID = sentMsg.Chat.ID
		state.ProgressMsg.MsgID = sentMsg.MessageID
		mu.Unlock()
	}

	// إضافة بقية الملصقات
	for i := 1; i < total; i++ {
		sticker := stickers[i]
		emoji := sticker.Emoji
		if emoji == "" {
			emoji = "🤖"
		}
		err := addStickerToSet(userID, packName, packType, sticker.FileID, emoji)
		if err != nil {
			log.Printf("فشل إضافة ملصق %d: %v", i, err)
			// نستمر في المحاولة
		}

		// تحديث شريط التقدم بعد كل إضافة
		percent := int(float64(i+1) / float64(total) * 100)
		progress := fmt.Sprintf("⏳ [%-10s] %d%% (%d/%d)", strings.Repeat("█", percent/10), percent, i+1, total)
		updateProgress(chatID, progress)
	}

	// بعد الانتهاء من جميع الملصقات، حذف رسالة التقدم وإرسال النتيجة
	// عند 100% نمسح الرسالة
	deleteProgressMessage(chatID)

	// إرسال رسالة النجاح
	link := fmt.Sprintf("https://t.me/addstickers/%s", packName)
	successMsg := fmt.Sprintf("✅ تم نسخ الحزمة بنجاح!\n\nالعنوان: %s\nالرابط: %s", fullTitle, link)
	bot.Send(tgbotapi.NewMessage(chatID, successMsg))

	// تنظيف الجلسة وإعادة القائمة الرئيسية
	mu.Lock()
	delete(sessions, chatID)
	mu.Unlock()
	sendMainMenu(chatID)
}

// updateProgress يقوم بتحديث زر التقدم.
func updateProgress(chatID int64, newText string) {
	mu.RLock()
	state, exists := sessions[chatID]
	mu.RUnlock()
	if !exists || state.ProgressMsg.MsgID == 0 {
		return
	}

	// إنشاء زر جديد بنفس البيانات الوهمية
	btn := tgbotapi.NewInlineKeyboardButtonData(newText, "progress_dummy")
	row := tgbotapi.NewInlineKeyboardRow(btn)
	markup := tgbotapi.NewInlineKeyboardMarkup(row)

	edit := tgbotapi.NewEditMessageReplyMarkup(state.ProgressMsg.ChatID, state.ProgressMsg.MsgID, markup)
	_, err := bot.Send(edit)
	if err != nil {
		log.Printf("فشل تحديث شريط التقدم: %v", err)
	}
}

// deleteProgressMessage يحذف رسالة التقدم.
func deleteProgressMessage(chatID int64) {
	mu.RLock()
	state, exists := sessions[chatID]
	mu.RUnlock()
	if !exists || state.ProgressMsg.MsgID == 0 {
		return
	}

	del := tgbotapi.NewDeleteMessage(state.ProgressMsg.ChatID, state.ProgressMsg.MsgID)
	_, err := bot.Send(del)
	if err != nil {
		log.Printf("فشل حذف رسالة التقدم: %v", err)
	}
	// إعادة تعيين المعرفات
	mu.Lock()
	state.ProgressMsg.ChatID = 0
	state.ProgressMsg.MsgID = 0
	mu.Unlock()
}

// ==================== معالج الويب هوك ====================

// Handler هو نقطة الدخول من Vercel.
func Handler(w http.ResponseWriter, r *http.Request) {
	// تهيئة البوت عند أول استدعاء (سيتم تهيئته مرة واحدة فقط بسبب المتغير العام)
	if bot == nil {
		token := os.Getenv("TELEGRAM_BOT_TOKEN")
		if token == "" {
			http.Error(w, "TELEGRAM_BOT_TOKEN not set", http.StatusInternalServerError)
			return
		}
		var err error
		bot, err = tgbotapi.NewBotAPI(token)
		if err != nil {
			log.Printf("فشل إنشاء البوت: %v", err)
			http.Error(w, "Bot init failed", http.StatusInternalServerError)
			return
		}
		bot.Debug = false // عيّن true للتطوير
	}

	// قراءة جسم الطلب
	var update tgbotapi.Update
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		log.Printf("خطأ في فك تشفير التحديث: %v", err)
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	// معالجة التحديث حسب نوعه
	if update.Message != nil {
		if update.Message.IsCommand() {
			switch update.Message.Command() {
			case "start":
				handleStart(update)
			default:
				// أوامر أخرى يمكن تجاهلها
			}
		} else {
			handleMessage(update)
		}
	} else if update.CallbackQuery != nil {
		handleCallbackQuery(update)
	}

	// الرد بنجاح
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "OK")
}
