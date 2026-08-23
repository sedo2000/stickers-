package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
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
	CurrentIdx      int
	CreatedPackName string
}

var userSessions = make(map[int64]*StickerPackSession)

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
		delete(userSessions, userId)
		sendHomeMenu(bot, msg.Chat.ID, msg.From.FirstName)
		return
	}

	session, exists := userSessions[userId]
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

		nextMsg := tgbotapi.NewMessage(msg.Chat.ID, "📦 ممتاز! الآن أرسل ملصقاً من الحزمة التي تود نسخها (سيتم النسخ بدفعات 20 ملصقاً):")
		nextMsg.ReplyMarkup = tgbotapi.ForceReply{ForceReply: true, Selective: true}
		bot.Send(nextMsg)
		return
	}

	// 2. استقبال الملصق وجلب الحزمة الأصلية ونسخ الدفعة الأولى (20 ملصقاً)
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

		loadingMsg, _ := bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "⏳ جاري قراءة الحزمة الأصلية..."))

		fileIDs, emojis, packType, err := fetchAllStickersFromSet(botToken, originalSetName)
		if loadingMsg.MessageID != 0 {
			bot.Request(tgbotapi.NewDeleteMessage(msg.Chat.ID, loadingMsg.MessageID))
		}

		if err != nil || len(fileIDs) == 0 {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "❌ فشل قراءة الحزمة الأصلية."))
			delete(userSessions, userId)
			sendHomeMenu(bot, msg.Chat.ID, msg.From.FirstName)
			return
		}

		session.AllFileIDs = fileIDs
		session.AllEmojis = emojis
		session.PackType = packType

		finalPackName := fmt.Sprintf("pack_%d_by_%d", time.Now().Unix()%100000, userId)
		session.CreatedPackName = finalPackName

		loadingCreate, _ := bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "⏳ جاري إنشاء الحزمة الجديدة في تليجرام..."))
		
		err = createNewPackWithFirstSticker(botToken, userId, finalPackName, session.UserCustomTitle, session.PackType, fileIDs[0], emojis[0])
		if loadingCreate.MessageID != 0 {
			bot.Request(tgbotapi.NewDeleteMessage(msg.Chat.ID, loadingCreate.MessageID))
		}

		if err != nil {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf("❌ خطأ بالإنشاء: %s", err.Error())))
			delete(userSessions, userId)
			sendHomeMenu(bot, msg.Chat.ID, msg.From.FirstName)
			return
		}

		endIndex := 20
		if endIndex > len(fileIDs) {
			endIndex = len(fileIDs)
		}

		if endIndex > 1 {
			addStickersBatch(botToken, userId, finalPackName, fileIDs, emojis, 1, endIndex)
		}

		session.CurrentIdx = endIndex

		if session.CurrentIdx >= len(fileIDs) {
			delete(userSessions, userId)
			doneText := fmt.Sprintf("🎉 **تم نسخ الحزمة بالكامل بنجاح!**\n\n🏷 عنوان الحزمة: `%s`\n🔗 رابط الحزمة:\nhttps://t.me/addstickers/%s", session.UserCustomTitle, finalPackName)
			msgOut := tgbotapi.NewMessage(msg.Chat.ID, doneText)
			msgOut.ParseMode = "Markdown"
			bot.Send(msgOut)
			sendHomeMenu(bot, msg.Chat.ID, msg.From.FirstName)
			return
		}

		session.Step = "awaiting_next_batch"

		nextText := fmt.Sprintf("✅ تم نسخ الدفعة الأولى (20 ملصقاً) بنجاح!\n\n🏷 عنوان الحزمة: `%s`\n\n👉 **أرسل أي ملصق من نفس الحزمة الآن لإكمال الدفعة التالية (20 ملصقاً إضافياً).**", session.UserCustomTitle)
		nextMsg := tgbotapi.NewMessage(msg.Chat.ID, nextText)
		nextMsg.ParseMode = "Markdown"
		nextMsg.ReplyMarkup = tgbotapi.ForceReply{ForceReply: true, Selective: true}
		bot.Send(nextMsg)
		return
	}

	// 3. استكمال الدفعات التالية (كل دفعة 20 ملصقاً)
	if session.Step == "awaiting_next_batch" {
		if msg.Sticker == nil {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "❌ يرجى إرسال ملصق للاستكمال."))
			return
		}

		bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "⏳ جاري إضافة الدفعة التالية من الملصقات..."))

		startIndex := session.CurrentIdx
		endIndex := startIndex + 20
		if endIndex > len(session.AllFileIDs) {
			endIndex = len(session.AllFileIDs)
		}

		addStickersBatch(botToken, userId, session.CreatedPackName, session.AllFileIDs, session.AllEmojis, startIndex, endIndex)
		session.CurrentIdx = endIndex

		if session.CurrentIdx >= len(session.AllFileIDs) {
			finalName := session.CreatedPackName
			finalTitle := session.UserCustomTitle
			delete(userSessions, userId)

			doneText := fmt.Sprintf("🎉 **تم الانتهاء من نسخ الحزمة بالكامل بجميع ملصقاتها!**\n\n🏷 عنوان الحزمة: `%s`\n🔗 رابط الحزمة النهائية:\nhttps://t.me/addstickers/%s", finalTitle, finalName)
			doneMsg := tgbotapi.NewMessage(msg.Chat.ID, doneText)
			doneMsg.ParseMode = "Markdown"
			bot.Send(doneMsg)
			sendHomeMenu(bot, msg.Chat.ID, msg.From.FirstName)
			return
		}

		nextText := fmt.Sprintf("✅ تمت إضافة دفعة جديدة بنجاح!\n\n👉 **أرسل ملصقاً مرة أخرى للاستكمال (الدفعة القادمة):**")
		nextMsg := tgbotapi.NewMessage(msg.Chat.ID, nextText)
		nextMsg.ReplyMarkup = tgbotapi.ForceReply{ForceReply: true, Selective: true}
		bot.Send(nextMsg)
		return
	}
}

func sendHomeMenu(bot *tgbotapi.BotAPI, chatID int64, firstName string) {
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🔄 نسخ حزمة ملصقات جديدة", "start_copy"),
		),
	)

	welcomeText := fmt.Sprintf("أهلاً بك يا %s! 👋\nأنا بوت مخصص حصرياً لـ **نسخ حزم الملصقات** بدفعات دقيقة (20 ملصقاً بكل دفعة) مع حفظ حقوقك في عنوان الحزمة (`%s`).\n\nاضغط الزر أدناه للبدء:", firstName, BotWatermark)
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
		userSessions[userId] = &StickerPackSession{Step: "awaiting_title"}

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

func addStickersBatch(botToken string, userID int64, newName string, fileIDs []string, emojis []string, start, end int) {
	addURL := fmt.Sprintf("https://api.telegram.org/bot%s/addStickerToSet", botToken)

	for i := start; i < end && i < len(fileIDs); i++ {
		addPayload := map[string]interface{}{
			"user_id": userID,
			"name":    newName,
			"sticker": map[string]interface{}{
				"sticker":    fileIDs[i],
				"emoji_list": []string{emojis[i]},
			},
		}

		addBytes, _ := json.Marshal(addPayload)
		addResp, err := http.Post(addURL, "application/json", bytes.NewBuffer(addBytes))
		if err == nil {
			addResp.Body.Close()
		}
		time.Sleep(120 * time.Millisecond)
	}
}
