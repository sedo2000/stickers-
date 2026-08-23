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

type StickerPackSession struct {
	Title    string
	Name     string
	Original string
	Step     string
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
		handleIncomingCallback(bot, update.CallbackQuery)
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "OK")
}

func handleIncomingMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message, botToken string) {
	if msg.IsCommand() && msg.Command() == "start" {
		sendHomeMenu(bot, msg.Chat.ID, msg.From.FirstName)
		return
	}

	if msg.ReplyToMessage != nil {
		handleForceReplySteps(bot, msg, botToken)
		return
	}
}

func sendHomeMenu(bot *tgbotapi.BotAPI, chatID int64, firstName string) {
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📦 حزماتي", "my_packs_menu"),
			tgbotapi.NewInlineKeyboardButtonData("🔄 نسخ حزمة ملصقات", "start_copy_flow"),
		),
	)

	welcomeText := fmt.Sprintf("أهلاً بك يا %s! 👋\nأنا بوت متخصص في استنساخ وتعديل حزم الملصقات 💡\n\nاختر من الأزرار أدناه:", firstName)
	reply := tgbotapi.NewMessage(chatID, welcomeText)
	reply.ReplyMarkup = keyboard

	bot.Send(reply)
}

func handleIncomingCallback(bot *tgbotapi.BotAPI, query *tgbotapi.CallbackQuery) {
	chatId := query.Message.Chat.ID
	userId := query.From.ID

	if query.Data == "start_copy_flow" {
		userSessions[userId] = &StickerPackSession{Step: "awaiting_title"}
		
		text := "الان ارسل اسم الحزمة الذي تريده 🗣"
		msg := tgbotapi.NewMessage(chatId, text)
		msg.ReplyMarkup = tgbotapi.ForceReply{ForceReply: true, Selective: true}
		bot.Send(msg)
		bot.Request(tgbotapi.NewCallback(query.ID, ""))

	} else if query.Data == "my_packs_menu" {
		text := "قسم حزماتي 📂\nأرسل رابط الحزمة المنسوخة هنا للتحكم بها أو حذف ملصقات منها."
		msg := tgbotapi.NewMessage(chatId, text)
		bot.Send(msg)
		bot.Request(tgbotapi.NewCallback(query.ID, ""))
	}
}

func handleForceReplySteps(bot *tgbotapi.BotAPI, msg *tgbotapi.Message, botToken string) {
	userId := msg.From.ID
	session, exists := userSessions[userId]
	if !exists {
		session = &StickerPackSession{Step: "awaiting_title"}
		userSessions[userId] = session
	}

	replyText := msg.ReplyToMessage.Text

	if strings.Contains(replyText, "الان ارسل اسم الحزمة الذي تريده") {
		session.Title = strings.TrimSpace(msg.Text)
		session.Step = "awaiting_name"

		nextMsg := tgbotapi.NewMessage(msg.Chat.ID, "الان ارسل معرف الحزمة الذي تريده 🗣")
		nextMsg.ReplyMarkup = tgbotapi.ForceReply{ForceReply: true, Selective: true}
		bot.Send(nextMsg)
		return
	}

	if strings.Contains(replyText, "الان ارسل معرف الحزمة الذي تريده") {
		session.Name = strings.TrimSpace(msg.Text)
		session.Step = "awaiting_sticker"

		nextMsg := tgbotapi.NewMessage(msg.Chat.ID, "ارسل ملصق من الحزمة التي تود نسخها 😃")
		nextMsg.ReplyMarkup = tgbotapi.ForceReply{ForceReply: true, Selective: true}
		bot.Send(nextMsg)
		return
	}

	if strings.Contains(replyText, "ارسل ملصق من الحزمة التي تود نسخها") {
		if msg.Sticker == nil {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "❌ يرجى إرسال ملصق صحيح من الحزمة المستهدفة."))
			return
		}

		originalSetName := msg.Sticker.SetName
		if originalSetName == "" {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "❌ هذا الملصق لا ينتمي لأي حزمة ملصقات."))
			return
		}

		session.Original = originalSetName
		botInfo, _ := bot.GetMe()
		finalPackName := fmt.Sprintf("%s_%d_by_%s", session.Name, time.Now().Unix()%10000, botInfo.UserName)

		loadingMsg, _ := bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "⏳ جاري إنشاء الحزمة ونسخ أول 15 ملصقاً..."))

		err := executeStickerCopyBatch(botToken, userId, originalSetName, session.Title, finalPackName, 0, 15)
		
		if loadingMsg.MessageID != 0 {
			bot.Request(tgbotapi.NewDeleteMessage(msg.Chat.ID, loadingMsg.MessageID))
		}

		if err != nil {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf("❌ حدث خطأ أثناء النسخ: %s", err.Error())))
			return
		}

		successText := fmt.Sprintf("تم نسخ الدفعة الأولى بنجاح ✅\n\nاسم الحزمة: %s\nرابط الحزمة: https://t.me/addstickers/%s\n\n- اعد ارسال الملصق لاكمال نسخ بقية الملصقات .", session.Title, finalPackName)
		
		session.Step = "awaiting_completion"
		session.Name = finalPackName

		outMsg := tgbotapi.NewMessage(msg.Chat.ID, successText)
		outMsg.ReplyMarkup = tgbotapi.ForceReply{ForceReply: true, Selective: true}
		bot.Send(outMsg)
		return
	}

	if strings.Contains(replyText, "اعد ارسال الملصق لاكمال نسخ بقية الملصقات") || session.Step == "awaiting_completion" {
		if msg.Sticker == nil {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "❌ يرجى إرسال الملصق المطلوب لإتمام استكمال الحزمة."))
			return
		}

		loadingMsg, _ := bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "⏳ جاري إكمال نسخ باقي الملصقات..."))
		
		err := executeStickerCopyBatch(botToken, userId, session.Original, session.Title, session.Name, 15, 100)
		
		if loadingMsg.MessageID != 0 {
			bot.Request(tgbotapi.NewDeleteMessage(msg.Chat.ID, loadingMsg.MessageID))
		}

		if err != nil {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf("⚠️ تنبيه: %s", err.Error())))
		}

		finalDoneText := fmt.Sprintf("🎉 **تم الانتهاء من نسخ الحزمة بالكامل وإضافة كل الملصقات بنجاح!**\n\nرابط الحزمة النهائية:\nhttps://t.me/addstickers/%s", session.Name)
		doneMsg := tgbotapi.NewMessage(msg.Chat.ID, finalDoneText)
		doneMsg.ParseMode = "Markdown"
		bot.Send(doneMsg)

		delete(userSessions, userId)
		sendHomeMenu(bot, msg.Chat.ID, msg.From.FirstName)
		return
	}
}

func executeStickerCopyBatch(botToken string, userID int64, originalSetName, newTitle, newName string, startIndex, maxCount int) error {
	getSetURL := fmt.Sprintf("https://api.telegram.org/bot%s/getStickerSet?name=%s", botToken, originalSetName)
	resp, err := http.Get(getSetURL)
	if err != nil {
		return fmt.Errorf("فشل الاتصال بجلب الحزمة الأصلية")
	}
	defer resp.Body.Close()

	var resStruct struct {
		Ok     bool `json:"ok"`
		Result struct {
			Stickers []struct {
				FileID      string `json:"file_id"`
				Emoji       string `json:"emoji"`
				IsVideo     bool   `json:"is_video"`
				IsAnimated  bool   `json:"is_animated"`
			} `json:"stickers"`
		} `json:"result"`
	}

	err = json.NewDecoder(resp.Body).Decode(&resStruct)
	if err != nil || !resStruct.Ok || len(resStruct.Result.Stickers) == 0 {
		return fmt.Errorf("فشل قراءة بيانات الحزمة الأصلية")
	}

	stickers := resStruct.Result.Stickers

	if startIndex == 0 {
		first := stickers[0]
		createURL := fmt.Sprintf("https://api.telegram.org/bot%s/createNewStickerSet", botToken)
		
		// تحديد نوع الحزمة (فيديو أو متحركة أو ثابتة) بناءً على الملصق الأول لتجنب خطأ تليجرام
		stickerField := "png_sticker"
		if first.IsVideo {
			stickerField = "video_sticker"
		} else if first.IsAnimated {
			stickerField = "tgs_sticker"
		}

		createPayload := map[string]interface{}{
			"user_id": userID,
			"name":    newName,
			"title":   newTitle,
			stickerField: first.FileID,
			"emojis":     first.Emoji,
		}

		bodyBytes, _ := json.Marshal(createPayload)
		creResp, err := http.Post(createURL, "application/json", bytes.NewBuffer(bodyBytes))
		if err != nil || creResp.StatusCode != http.StatusOK {
			// محاولة قراءة سبب الخطأ من تيليجرام
			respBody, _ := io.ReadAll(creResp.Body)
			return fmt.Errorf("تليجرام رفض الإنشاء: %s", string(respBody))
		}
		creResp.Body.Close()
		startIndex = 1
	}

	endIndex := startIndex + maxCount
	if endIndex > len(stickers) {
		endIndex = len(stickers)
	}

	addURL := fmt.Sprintf("https://api.telegram.org/bot%s/addStickerToSet", botToken)
	for i := startIndex; i < endIndex; i++ {
		current := stickers[i]
		
		addPayload := map[string]interface{}{
			"user_id": userID,
			"name":    newName,
			"sticker": map[string]interface{}{
				"sticker":    current.FileID,
				"emoji_list": []string{current.Emoji},
			},
		}

		addBytes, _ := json.Marshal(addPayload)
		addResp, err := http.Post(addURL, "application/json", bytes.NewBuffer(addBytes))
		if err == nil {
			addResp.Body.Close()
		}
		time.Sleep(100 * time.Millisecond)
	}

	return nil
}
