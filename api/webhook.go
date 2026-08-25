package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// الثوابت المطلوبة للحقوق واسم البوت
const BotWatermark = "@I5I5Ie"
const TargetBotUsername = "Cut88bot"

// هيكل الجلسة لكل مستخدم
type StickerPackSession struct {
	Step            string
	Mode            string
	UserCustomTitle string
	OriginalSetName string
	PackType        string
	AllFileIDs      []string
	AllEmojis       []string
	CreatedPackName string
	TotalCount      int
	ChatID          int64
}

// إدارة الجلسات في الذاكرة مع حماية التزامن (Mutex)
var userSessions = make(map[int64]*StickerPackSession)
var sessionsLock sync.Mutex

// نقطة الدخول الرئيسية للـ Webhook
func Handler(w http.ResponseWriter, r *http.Request) {
	botToken := os.Getenv("BOT_TOKEN")
	if botToken == "" {
		http.Error(w, "Bot token not configured", http.StatusInternalServerError)
		return
	}

	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		http.Error(w, "Failed to create bot", http.StatusInternalServerError)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	var update tgbotapi.Update
	err = json.Unmarshal(body, &update)
	if err != nil {
		http.Error(w, "Failed to unmarshal update", http.StatusBadRequest)
		return
	}

	// توجيه التحديث سواء كان رسالة أو ضغطة زر
	if update.Message != nil {
		handleIncomingMessage(bot, update.Message, botToken)
	} else if update.CallbackQuery != nil {
		handleIncomingCallback(bot, update.CallbackQuery, botToken)
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "OK")
}

// معالجة الرسائل الواردة
func handleIncomingMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message, botToken string) {
	userId := msg.From.ID

	// أمر البداية /start
	if msg.IsCommand() && msg.Command() == "start" {
		clearSession(userId)
		sendHomeMenu(bot, msg.Chat.ID, msg.From.FirstName)
		return
	}

	sessionsLock.Lock()
	session, exists := userSessions[userId]
	sessionsLock.Unlock()

	// إذا لم تكن هناك جلسة، نعرض القائمة الرئيسية
	if !exists {
		sendHomeMenu(bot, msg.Chat.ID, msg.From.FirstName)
		return
	}

	// خطوة 1: استقبال عنوان الحزمة
	if session.Step == "awaiting_title" {
		if msg.Text == "" {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "❌ يرجى إرسال اسم نصي صحيح للحزمة."))
			return
		}

		// إضافة الحقوق للعنوان
		customTitle := strings.TrimSpace(msg.Text)
		session.UserCustomTitle = fmt.Sprintf("%s | %s", customTitle, BotWatermark)
		session.Step = "awaiting_sticker"

		modeText := "الثابتة (PNG)"
		if session.Mode == "animated" {
			modeText = "المتحركة (TGS / Video)"
		}

		nextMsg := tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf("📦 ممتاز! الآن أرسل ملصقاً من حزمة **%s** التي تود نسخها:", modeText))
		nextMsg.ParseMode = "Markdown"
		bot.Send(nextMsg)
		return
	}

	// خطوة 2: استقبال الملصق للنسخ
	if session.Step == "awaiting_sticker" {
		if msg.Sticker == nil {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "❌ يرجى إرسال ملصق صحيح من تليجرام."))
			return
		}

		originalSetName := msg.Sticker.SetName
		if originalSetName == "" {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "❌ هذا الملصق لا ينتمي لأي حزمة."))
			return
		}

		session.OriginalSetName = originalSetName
		session.ChatID = msg.Chat.ID

		loadingMsg, _ := bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "⏳ جاري فحص الحزمة الأصلية..."))

		fileIDs, emojis, packType, err := fetchAllStickersFromSet(botToken, originalSetName)
		if loadingMsg.MessageID != 0 {
			bot.Request(tgbotapi.NewDeleteMessage(msg.Chat.ID, loadingMsg.MessageID))
		}

		if err != nil || len(fileIDs) == 0 {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "❌ فشل قراءة الحزمة الأصلية."))
			clearSession(userId)
			sendHomeMenu(bot, msg.Chat.ID, msg.From.FirstName)
			return
		}

		// التحقق من نوع الحزمة بناءً على اختيار المستخدم
		if session.Mode == "static" && packType != "static" {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "❌ أنت في قسم **الملصقات الثابتة**، لكن الحزمة المرسلة متحركة أو مرئية! يرجى العودة للقسم المناسب."))
			clearSession(userId)
			sendHomeMenu(bot, msg.Chat.ID, "")
			return
		}

		if session.Mode == "animated" && packType == "static" {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "❌ أنت في قسم **الملصقات المتحركة**، لكن الحزمة المرسلة ثابتة (عادية)! يرجى العودة للقسم المناسب."))
			clearSession(userId)
			sendHomeMenu(bot, msg.Chat.ID, "")
			return
		}

		session.AllFileIDs = fileIDs
		session.AllEmojis = emojis
		session.PackType = packType
		session.TotalCount = len(fileIDs)

		// توليد اسم الحزمة بالشكل المطلوب
		session.CreatedPackName = fmt.Sprintf("p%d_by_%s", time.Now().UnixNano()%1000000, TargetBotUsername)
		session.Step = "processing"

		// بدء عملية المعالجة والرفع
		go processAndCopyAllStickers(bot, botToken, userId, session)
		return
	}
}

// دالة لمعالجة ورفع الملصقات مع تحديث شريط التقدم
func processAndCopyAllStickers(bot *tgbotapi.BotAPI, botToken string, userId int64, session *StickerPackSession) {
	total := session.TotalCount

	// إرسال رسالة شريط التقدم الأولية
	initialKeyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("⏳ [░░░░░░░░░░] 0% (1/"+fmt.Sprintf("%d", total)+")", "loading_status"),
		),
	)

	msgConfig := tgbotapi.NewMessage(session.ChatID, "📦 **جاري معالجة ورفع الحزمة...**")
	msgConfig.ParseMode = "Markdown"
	msgConfig.ReplyMarkup = initialKeyboard
	statusMsg, err := bot.Send(msgConfig)
	
	var statusMsgID int
	if err == nil {
		statusMsgID = statusMsg.MessageID
	}

	// إنشاء الحزمة باستخدام أول ملصق
	err = createNewPackWithFirstSticker(botToken, userId, session.CreatedPackName, session.UserCustomTitle, session.PackType, session.AllFileIDs[0], session.AllEmojis[0])
	if err != nil {
		if statusMsgID != 0 {
			bot.Request(tgbotapi.NewDeleteMessage(session.ChatID, statusMsgID))
		}
		bot.Send(tgbotapi.NewMessage(session.ChatID, fmt.Sprintf("❌ خطأ أثناء إنشاء الحزمة: %s", err.Error())))
		clearSession(userId)
		sendHomeMenu(bot, session.ChatID, "")
		return
	}

	addURL := fmt.Sprintf("https://api.telegram.org/bot%s/addStickerToSet", botToken)

	// رفع باقي الملصقات وتحديث الشريط
	for i := 1; i < total; i++ {
		addPayload := map[string]interface{}{
			"user_id": userId,
			"name":    session.CreatedPackName,
		}

		// الهيكل الدقيق لتفادي خطأ 400 (Bad Request)
		if session.PackType == "video" {
			addPayload["video_sticker"] = map[string]interface{}{
				"sticker":    session.AllFileIDs[i],
				"emoji_list": []string{session.AllEmojis[i]},
			}
		} else if session.PackType == "animated" {
			addPayload["tgs_sticker"] = map[string]interface{}{
				"sticker": session.AllFileIDs[i],
			}
			addPayload["emojis"] = session.AllEmojis[i]
		} else {
			addPayload["png_sticker"] = session.AllFileIDs[i]
			addPayload["emojis"] = session.AllEmojis[i]
		}

		addBytes, _ := json.Marshal(addPayload)
		resp, err := http.Post(addURL, "application/json", bytes.NewBuffer(addBytes))
		if err == nil {
			resp.Body.Close()
		}

		// تحديث الشريط التفاعلي (بدون time.Sleep لتحقيق أقصى سرعة)
		processed := i + 1
		percent := (processed * 100) / total

		if statusMsgID != 0 && processed < total {
			filled := percent / 10
			if filled > 10 { filled = 10 }
			bar := strings.Repeat("█", filled) + strings.Repeat("░", 10-filled)

			btnText := fmt.Sprintf("⏳ [%s] %d%% (%d/%d)", bar, percent, processed, total)
			newKeyboard := tgbotapi.NewInlineKeyboardMarkup(
				tgbotapi.NewInlineKeyboardRow(
					tgbotapi.NewInlineKeyboardButtonData(btnText, "loading_status"),
				),
			)
			editMarkup := tgbotapi.NewEditMessageReplyMarkup(session.ChatID, statusMsgID, newKeyboard)
			bot.Send(editMarkup)
		}
	}

	// عند الوصول لـ 100%، نقوم بحذف رسالة التقدم بالكامل
	if statusMsgID != 0 {
		bot.Request(tgbotapi.NewDeleteMessage(session.ChatID, statusMsgID))
	}

	// إرسال رسالة الإنجاز النهائية مع الرابط الدقيق
	doneText := fmt.Sprintf("🎉 **تم نسخ الحزمة بالكامل بنجاح!**\n\n🏷 عنوان الحزمة: `%s`\n🔗 رابط الحزمة:\nhttps://t.me/addstickers/%s", session.UserCustomTitle, session.CreatedPackName)
	doneMsg := tgbotapi.NewMessage(session.ChatID, doneText)
	doneMsg.ParseMode = "Markdown"
	bot.Send(doneMsg)

	// مسح الجلسة والعودة للرئيسية
	clearSession(userId)
	sendHomeMenu(bot, session.ChatID, "")
}

// دالة جلب كل ملصقات الحزمة وتحديد نوعها
func fetchAllStickersFromSet(botToken, originalSetName string) ([]string, []string, string, error) {
	getSetURL := fmt.Sprintf("https://api.telegram.org/bot%s/getStickerSet?name=%s", botToken, originalSetName)
	resp, err := http.Get(getSetURL)
	if err != nil {
		return nil, nil, "", err
	}
	defer resp.Body.Close()

	var resStruct struct {
		Ok     bool `json:"ok"`
		Result struct {
			Stickers []struct {
				FileID     string `json:"file_id"`
				Emoji      string `json:"emoji"`
				IsVideo    bool   `json:"is_video"`
				IsAnimated bool   `json:"is_animated"`
			} `json:"stickers"`
		} `json:"result"`
	}

	err = json.NewDecoder(resp.Body).Decode(&resStruct)
	if err != nil || !resStruct.Ok || len(resStruct.Result.Stickers) == 0 {
		return nil, nil, "", fmt.Errorf("empty set")
	}

	var fileIDs []string
	var emojis []string
	packType := "static" // Default

	first := resStruct.Result.Stickers[0]
	if first.IsVideo {
		packType = "video"
	} else if first.IsAnimated {
		packType = "animated"
	}

	for _, s := range resStruct.Result.Stickers {
		emoji := s.Emoji
		if emoji == "" {
			emoji = "⭐"
		}
		fileIDs = append(fileIDs, s.FileID)
		emojis = append(emojis, emoji)
	}

	return fileIDs, emojis, packType, nil
}

// دالة إنشاء الحزمة الجديدة بأول ملصق (نفس الهيكل الدقيق لتفادي 400)
func createNewPackWithFirstSticker(botToken string, userID int64, newName, newTitle, packType, fileID, emoji string) error {
	createURL := fmt.Sprintf("https://api.telegram.org/bot%s/createNewStickerSet", botToken)

	createPayload := map[string]interface{}{
		"user_id": userID,
		"name":    newName,
		"title":   newTitle,
	}

	if packType == "video" {
		createPayload["video_sticker"] = map[string]interface{}{
			"sticker":    fileID,
			"emoji_list": []string{emoji},
		}
	} else if packType == "animated" {
		createPayload["tgs_sticker"] = map[string]interface{}{
			"sticker": fileID,
		}
		createPayload["emojis"] = emoji
	} else {
		createPayload["png_sticker"] = fileID
		createPayload["emojis"] = emoji
	}

	bodyBytes, _ := json.Marshal(createPayload)
	creResp, err := http.Post(createURL, "application/json", bytes.NewBuffer(bodyBytes))
	if err != nil {
		return err
	}
	defer creResp.Body.Close()

	if creResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(creResp.Body)
		return fmt.Errorf("%s", string(respBody))
	}
	return nil
}

// معالجة ضغطات الأزرار (Callbacks)
func handleIncomingCallback(bot *tgbotapi.BotAPI, query *tgbotapi.CallbackQuery, botToken string) {
	chatId := query.Message.Chat.ID
	userId := query.From.ID
	data := query.Data

	// للرد على زر التقدم حتى لا يبقى قيد التحميل
	if data == "loading_status" {
		bot.Request(tgbotapi.NewCallback(query.ID, "🔄 العملية جارية، يرجى الانتظار..."))
		return
	}
	bot.Request(tgbotapi.NewCallback(query.ID, ""))

	if data == "copy_static" || data == "copy_animated" {
		mode := "static"
		modeName := "ثابتة (PNG)"
		if data == "copy_animated" {
			mode = "animated"
			modeName = "متحركة (TGS / Video)"
		}

		sessionsLock.Lock()
		userSessions[userId] = &StickerPackSession{
			Step: "awaiting_title",
			Mode: mode,
		}
		sessionsLock.Unlock()

		msg := tgbotapi.NewMessage(chatId, fmt.Sprintf("📝 أنت الآن في قسم نسخ الملصقات **%s**.\nأرسل الآن **اسم الحزمة** الذي تريده:", modeName))
		msg.ParseMode = "Markdown"
		bot.Send(msg)
		return
	}

	if data == "my_packs" {
		packsKeyboard := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("✏️ تعديل عنوان حزمة", "edit_title"),
				tgbotapi.NewInlineKeyboardButtonData("🗑 حذف ملصق من حزمة", "delete_sticker"),
			),
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("🔙 رجوع للقائمة الرئيسية", "back_home"),
			),
		)
		msg := tgbotapi.NewMessage(chatId, "📁 **قسم حزماتي (الإدارة الداخلية):**\nاختر العملية التي تريد تنفيذها:")
		msg.ParseMode = "Markdown"
		msg.ReplyMarkup = packsKeyboard
		bot.Send(msg)
		return
	}

	if data == "edit_title" {
		msg := tgbotapi.NewMessage(chatId, "✏️ **لتعديل عنوان الحزمة:**\nأرسل اسم الحزمة أو رابطها متبوعاً بالعنوان الجديد.\n*(ميزة قيد التطوير)*")
		msg.ParseMode = "Markdown"
		bot.Send(msg)
		return
	}

	if data == "delete_sticker" {
		msg := tgbotapi.NewMessage(chatId, "🗑 **لحذف ملصق:**\nأرسل الملصق المراد حذفه مباشرة إلى البوت هنا لمعالجته.\n*(ميزة قيد التطوير)*")
		msg.ParseMode = "Markdown"
		bot.Send(msg)
		return
	}

	if data == "back_home" {
		clearSession(userId)
		sendHomeMenu(bot, chatId, "")
		return
	}
}

// عرض القائمة الرئيسية
func sendHomeMenu(bot *tgbotapi.BotAPI, chatID int64, firstName string) {
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

	welcomeText := "أهلاً بك! 👋\nاختر القسم المناسب لنسخ حزم الملصقات أدناه:"
	if firstName != "" {
		welcomeText = fmt.Sprintf("أهلاً بك يا %s! 👋\nاختر القسم المناسب لنسخ حزم الملصقات أدناه:", firstName)
	}

	msg := tgbotapi.NewMessage(chatID, welcomeText)
	msg.ReplyMarkup = keyboard
	bot.Send(msg)
}

// مسح الجلسة بشكل آمن
func clearSession(userId int64) {
	sessionsLock.Lock()
	delete(userSessions, userId)
	sessionsLock.Unlock()
}
