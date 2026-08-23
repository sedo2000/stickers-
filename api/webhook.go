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

		botMe, err := bot.GetMe()
		botUsername := "stickersbot"
		if err == nil && botMe.UserName != "" {
			botUsername = botMe.UserName
		}

		finalPackName := fmt.Sprintf("p%d_by_%s", time.Now().UnixNano()%1000000, botUsername)
		session.CreatedPackName = finalPackName
		session.Step = "processing"

		go processAndCopyAllStickersLightning(bot, botToken, userId, session)
		return
	}
}

func processAndCopyAllStickersLightning(bot *tgbotapi.BotAPI, botToken string, userId int64, session *StickerPackSession) {
	total := session.TotalCount
	
	initialText := "🚀 **جاري النسخ بأقصى سرعة صاروخية...**"
	initialKeyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("⚡ Turbo: 1/%d (0%%)", total), "loading_status"),
		),
	)

	msgConfig := tgbotapi.NewMessage(session.ChatID, initialText)
	msgConfig.ReplyMarkup = initialKeyboard
	statusMsg, err := bot.Send(msgConfig)
	var statusMsgID int
	if err == nil {
		statusMsgID = statusMsg.MessageID
	}

	// إنشاء الحزمة بالملصق الأول
	err = createNewPackWithFirstSticker(botToken, userId, session.CreatedPackName, session.UserCustomTitle, session.PackType, session.AllFileIDs[0], session.AllEmojis[0])
	if err != nil {
		if statusMsgID != 0 {
			bot.Request(tgbotapi.NewDeleteMessage(session.ChatID, statusMsgID))
		}
		bot.Send(tgbotapi.NewMessage(session.ChatID, fmt.Sprintf("❌ خطأ أثناء إنشاء الحزمة: %s", err.Error())))
		sessionsLock.Lock()
		delete(userSessions, userId)
		sessionsLock.Unlock()
		sendHomeMenu(bot, session.ChatID, "")
		return
	}

	addURL := fmt.Sprintf("https://api.telegram.org/bot%s/addStickerToSet", botToken)

	// رفع باقي الملصقات بأقصى سرعة بدون أي تأخير زمني (No Sleep)
	for i := 1; i < total; i++ {
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

		processed := i + 1
		percent := (processed * 100) / total

		// تحديث الزر كل 10 ملصقات أو عند النهاية لضمان عدم حدوث تداخل أو إبطاء للعملية
		if statusMsgID != 0 && (i%10 == 0 || processed == total) {
			btnText := fmt.Sprintf("⚡ Turbo: %d/%d (%d%%)", processed, total, percent)
			if processed == total {
				btnText = fmt.Sprintf("🔥 اكتمل النسخ بنجاح! (%d/%d - 100%)", processed, total)
			}

			newKeyboard := tgbotapi.NewInlineKeyboardMarkup(
				tgbotapi.NewInlineKeyboardRow(
					tgbotapi.NewInlineKeyboardButtonData(btnText, "loading_status"),
				),
			)
			editMarkup := tgbotapi.NewEditMessageReplyMarkup(session.ChatID, statusMsgID, newKeyboard)
			bot.Send(editMarkup)
		}
	}

	if statusMsgID != 0 {
		bot.Request(tgbotapi.NewDeleteMessage(session.ChatID, statusMsgID))
	}

	doneText := fmt.Sprintf("🎉 **تم نسخ الحزمة بالكامل بسرعة البرق والصاروخ!**\n\n🏷 عنوان الحزمة: `%s`\n🔗 رابط الحزمة:\nhttps://t.me/addstickers/%s", session.UserCustomTitle, session.CreatedPackName)
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

	welcomeText := "أهلاً بك! 👋\nأنا بوت مخصص لـ **نسخ حزم الملصقات بأقصى سرعة صاروخية** مع زر شفاف متطور.\n\nاضغط الزر أدناه للبدء:"
	if firstName != "" {
		welcomeText = fmt.Sprintf("أهلاً بك يا %s! 👋\nأنا بوت مخصص لـ **نسخ حزم الملصقات بأقصى سرعة صاروخية** مع زر شفاف متطور.\n\nاضغط الزر أدناه للبدء:", firstName)
	}

	msg := tgbotapi.NewMessage(chatID, welcomeText)
	msg.ReplyMarkup = keyboard
	bot.Send(msg)
}

func handleIncomingCallback(bot *tgbotapi.BotAPI, query *tgbotapi.CallbackQuery, botToken string) {
	chatId := query.Message.Chat.ID
	userId := query.From.ID
	data := query.Data

	if data == "loading_status" {
		callbackConfig := tgbotapi.NewCallback(query.ID, "🚀 البوت يعمل بأقصى سرعة فائقة الآن، انتظر لحظات وتكتمل الحزمة!")
		bot.Request(callbackConfig)
		return
	}

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
