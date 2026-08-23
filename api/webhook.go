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

const BotWatermark = "@I5I5Ie"

type StickerPackSession struct {
	Step            string
	UserCustomTitle string
	OriginalSetName string
	PackType        string
	AllFileIDs      []string
	AllEmojis       []string
	CreatedPackName string
	IsProcessing    bool
	ProcessedCount  int
	TotalCount      int
	ChatID          int64
	StatusMessageID int
}

var userSessions = make(map[int64]*StickerPackSession)
var sessionsLock sync.Mutex

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

	if update.Message != nil {
		handleIncomingMessage(bot, update.Message, botToken)
	} else if update.CallbackQuery != nil {
		handleIncomingCallback(bot, update.CallbackQuery, botToken)
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "OK")
}

func handleIncomingMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message, botToken string) {
	userId := msg.From.ID

	if msg.IsCommand() && msg.Command() == "start" {
		sessionsLock.Lock()
		delete(userSessions, userId)
		sessionsLock.Unlock()
		sendHomeMenu(bot, msg.Chat.ID, msg.From.FirstName)
		return
	}

	sessionsLock.Lock()
	session, exists := userSessions[userId]
	sessionsLock.Unlock()

	if !exists {
		sendHomeMenu(bot, msg.Chat.ID, msg.From.FirstName)
		return
	}

	// 1. استقبال اسم الحزمة ودمجه مع يوزر الحقوق @I5I5Ie
	if session.Step == "awaiting_title" {
		if msg.Text == "" {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "❌ يرجى إرسال اسم نصي صحيح للحزمة."))
			return
		}
		
		customTitle := strings.TrimSpace(msg.Text)
		session.UserCustomTitle = fmt.Sprintf("%s | %s", customTitle, BotWatermark)
		session.Step = "awaiting_sticker"

		nextMsg := tgbotapi.NewMessage(msg.Chat.ID, "📦 ممتاز! الآن أرسل ملصقاً من الحزمة التي تود نسخها:")
		nextMsg.ReplyMarkup = tgbotapi.ForceReply{ForceReply: true, Selective: true}
		bot.Send(nextMsg)
		return
	}

	// 2. استقبال الملصق وجلب الحزمة وبدء النسخ التلقائي بالكامل
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

		loadingMsg, _ := bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "⏳ جاري قراءة الحزمة الأصلية وجلب جميع الملصقات..."))

		fileIDs, emojis, packType, err := fetchAllStickersFromSet(botToken, originalSetName)
		if loadingMsg.MessageID != 0 {
			bot.Request(tgbotapi.NewDeleteMessage(msg.Chat.ID, loadingMsg.MessageID))
		}

		if err != nil || len(fileIDs) == 0 {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "❌ فشل قراءة الحزمة الأصلية."))
			sessionsLock.Lock()
			delete(userSessions, userId)
			sessionsLock.Unlock()
			sendHomeMenu(bot, msg.Chat.ID, msg.From.FirstName)
			return
		}

		session.AllFileIDs = fileIDs
		session.AllEmojis = emojis
		session.PackType = packType
		session.TotalCount = len(fileIDs)

		// جلب يوزر البوت الحقيقي للتأكد من تطابق الشروط بالشرطة السفلية _by_
		botMe, err := bot.GetMe()
		botUsername := "stickersbot"
		if err == nil && botMe.UserName != "" {
			botUsername = botMe.UserName
		}

		finalPackName := fmt.Sprintf("p%d_by_%s", time.Now().UnixNano()%1000000, botUsername)
		session.CreatedPackName = finalPackName
		session.Step = "processing"

		// بدء عملية النسخ في الخلفية مع التحديث الحي للمستخدم
		go processAndCopyAllStickers(bot, botToken, userId, session)
		return
	}
}

func processAndCopyAllStickers(bot *tgbotapi.BotAPI, botToken string, userId int64, session *StickerPackSession) {
	// 1. إنشاء الحزمة بالملصق الأول
	err := createNewPackWithFirstSticker(botToken, userId, session.CreatedPackName, session.UserCustomTitle, session.PackType, session.AllFileIDs[0], session.AllEmojis[0])
	if err != nil {
		msg := tgbotapi.NewMessage(session.ChatID, fmt.Sprintf("❌ خطأ أثناء إنشاء الحزمة: %s", err.Error()))
		bot.Send(msg)
		sessionsLock.Lock()
		delete(userSessions, userId)
		sessionsLock.Unlock()
		sendHomeMenu(bot, session.ChatID, "")
		return
	}

	// إرسال رسالة حالة يتم تعديلها لاحقاً
	statusMsg, err := bot.Send(tgbotapi.NewMessage(session.ChatID, fmt.Sprintf("⏳ جاري نسخ الحزمة... (1/%d - 0%%)", session.TotalCount)))
	var statusMsgID int
	if err == nil {
		statusMsgID = statusMsg.MessageID
	}

	// 2. رفع باقي الملصقات دفعة واحدة مع تحديث العدّاد
	addURL := fmt.Sprintf("https://api.telegram.org/bot%s/addStickerToSet", botToken)
	
	for i := 1; i < len(session.AllFileIDs); i++ {
		addPayload := map[string]interface{}{
			"user_id": userId,
			"name":    session.CreatedPackName,
			"sticker": map[string]interface{}{
				"sticker":    session.AllFileIDs[i],
				"emoji_list": []string{session.AllEmojis[i]},
			},
		}

		addBytes, _ := json.Marshal(addPayload)
		resp, err := http.Post(addURL, "application/json", bytes.NewBuffer(addBytes))
		if err == nil {
			resp.Body.Close()
		}

		session.ProcessedCount = i + 1
		
		// تحديث رسالة التقدم كل 5 ملصقات لتجنب حظر تليجرام لرسائل التعديل الكثيرة
		if statusMsgID != 0 && (i%5 == 0 || i == len(session.AllFileIDs)-1) {
			percent := (session.ProcessedCount * 100) / session.TotalCount
			editText := fmt.Sprintf("⏳ جاري نسخ الحزمة... (%d/%d - %d%%)", session.ProcessedCount, session.TotalCount, percent)
			editMsg := tgbotapi.NewEditMessageText(session.ChatID, statusMsgID, editText)
			bot.Send(editMsg)
		}

		time.Sleep(150 * time.Millisecond) // تأهيل خفيف لمنع ضغط السيرفر
	}

	// حذف رسالة الحالة المؤقتة إذا وجدت
	if statusMsgID != 0 {
		bot.Request(tgbotapi.NewDeleteMessage(session.ChatID, statusMsgID))
	}

	// إرسال النتيجة النهائية برابط صحيح تماماً
	doneText := fmt.Sprintf("🎉 **تم نسخ الحزمة بالكامل بنجاح!**\n\n🏷 عنوان الحزمة: `%s`\n🔗 رابط الحزمة:\nhttps://t.me/addstickers/%s", session.UserCustomTitle, session.CreatedPackName)
	doneMsg := tgbotapi.NewMessage(session.ChatID, doneText)
	doneMsg.ParseMode = "Markdown"
	bot.Send(doneMsg)

	sessionsLock.Lock()
	delete(userSessions, userId)
	sessionsLock.Unlock()

	sendHomeMenu(bot, session.ChatID, "")
}

func sendHomeMenu(bot *tgbotapi.BotAPI, chatID int64, firstName string) {
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🔄 نسخ حزمة ملصقات جديدة", "start_copy"),
		),
	)

	welcomeText := "أهلاً بك! 👋\nأنا بوت مخصص لـ **نسخ حزم الملصقات بالكامل تلقائياً** مع حفظ حقوقك في عنوان الحزمة.\n\nاضغط الزر أدناه للبدء:"
	if firstName != "" {
		welcomeText = fmt.Sprintf("أهلاً بك يا %s! 👋\nأنا بوت مخصص لـ **نسخ حزم الملصقات بالكامل تلقائياً** مع حفظ حقوقك في عنوان الحزمة.\n\nاضغط الزر أدناه للبدء:", firstName)
	}

	msg := tgbotapi.NewMessage(chatID, welcomeText)
	msg.ReplyMarkup = keyboard
	bot.Send(msg)
}

func handleIncomingCallback(bot *tgbotapi.BotAPI, query *tgbotapi.CallbackQuery, botToken string) {
	chatId := query.Message.Chat.ID
	userId := query.From.ID
	data := query.Data

	bot.Request(tgbotapi.NewCallback(query.ID, ""))

	if data == "start_copy" {
		sessionsLock.Lock()
		userSessions[userId] = &StickerPackSession{Step: "awaiting_title"}
		sessionsLock.Unlock()

		msg := tgbotapi.NewMessage(chatId, "📝 أرسل الآن **اسم الحزمة** الذي تريده (سيتم إضافة يوزر حقوقك تلقائياً بجانبه):")
		msg.ParseMode = "Markdown"
		msg.ReplyMarkup = tgbotapi.ForceReply{ForceReply: true, Selective: true}
		bot.Send(msg)
	}
}

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
	packType := "png"

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

func createNewPackWithFirstSticker(botToken string, userID int64, newName, newTitle, packType, fileID, emoji string) error {
	createURL := fmt.Sprintf("https://api.telegram.org/bot%s/createNewStickerSet", botToken)

	stickerField := "png_sticker"
	if packType == "video" {
		stickerField = "video_sticker"
	} else if packType == "animated" {
		stickerField = "tgs_sticker"
	}

	createPayload := map[string]interface{}{
		"user_id":      userID,
		"name":         newName,
		"title":        newTitle,
		stickerField:   fileID,
		"emojis":       emoji,
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
