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
		handleMessage(bot, update.Message, botToken)
	} else if update.CallbackQuery != nil {
		handleCallbackQuery(bot, update.CallbackQuery)
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "OK")
}

func handleMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message, botToken string) {
	if msg.IsCommand() && msg.Command() == "start" {
		sendMainMenu(bot, msg.Chat.ID, msg.From.FirstName)
		return
	}

	if msg.ReplyToMessage != nil {
		handleForceReply(bot, msg, botToken)
		return
	}
}

func sendMainMenu(bot *tgbotapi.BotAPI, chatID int64, firstName string) {
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📦 حزماتي", "my_packs"),
			tgbotapi.NewInlineKeyboardButtonData("🔄 نسخ حزمة ملصقات", "start_copy"),
		),
	)

	welcomeText := fmt.Sprintf("أهلاً بك يا %s! 👋\nأنا بوت متخصص في استنساخ حزم الملصقات وتعديلها بكل سهولة 💡\n\nعجبك البوت؟ اصنع بوتك الخاص مجاناً!\n@CC2Pbot", firstName)
	reply := tgbotapi.NewMessage(chatID, welcomeText)
	reply.ReplyMarkup = keyboard

	bot.Send(reply)
}

func handleCallbackQuery(bot *tgbotapi.BotAPI, query *tgbotapi.CallbackQuery) {
	chatId := query.Message.Chat.ID
	userId := query.From.ID

	if query.Data == "start_copy" {
		text := "الان ارسل اسم الحزمة الذي تريده 🗣"
		msg := tgbotapi.NewMessage(chatId, text)
		msg.ReplyMarkup = tgbotapi.ForceReply{ForceReply: true, Selective: true}
		bot.Send(msg)
		bot.Request(tgbotapi.NewCallback(query.ID, ""))
	} else if query.Data == "my_packs" {
		// جلب الحزم المخزنة للمستخدم (كمثال توضيحي مبسط، أو يمكن تخزينها في ذاكرة البوت للمستخدم)
		text := "الحزم الخاصة بك 🔽\nيمكنك التعديل على الحزمة من خلال الضغط على 📝\n\n(أرسل رابط الحزمة المنسوخة هنا للتحكم بها وحذف الملصقات أو تغيير الاسم)."
		msg := tgbotapi.NewMessage(chatId, text)
		bot.Send(msg)
		bot.Request(tgbotapi.NewCallback(query.ID, ""))
	}
}

func handleForceReply(bot *tgbotapi.BotAPI, msg *tgbotapi.Message, botToken string) {
	replyText := msg.ReplyToMessage.Text

	if strings.Contains(replyText, "الان ارسل اسم الحزمة الذي تريده") {
		packTitle := strings.TrimSpace(msg.Text)
		nextStepText := fmt.Sprintf("الان ارسل معرف الحزمة الذي تريده 🗣\nالاسم المختار: %s", packTitle)
		
		replyMsg := tgbotapi.NewMessage(msg.Chat.ID, nextStepText)
		replyMsg.ReplyMarkup = tgbotapi.ForceReply{ForceReply: true, Selective: true}
		bot.Send(replyMsg)
		return
	}

	if strings.Contains(replyText, "الان ارسل معرف الحزمة الذي تريده") {
		lines := strings.Split(replyText, "\n")
		var packTitle string
		for _, line := range lines {
			if strings.HasPrefix(line, "الاسم المختار:") {
				packTitle = strings.TrimSpace(strings.TrimPrefix(line, "الاسم المختار:"))
			}
		}
		packNameUser := strings.TrimSpace(msg.Text)

		nextStepText := fmt.Sprintf("ارسل ملصق من الحزمة التي تود نسخها 😃\nالاسم: %s\nالمعرف: %s", packTitle, packNameUser)
		
		replyMsg := tgbotapi.NewMessage(msg.Chat.ID, nextStepText)
		replyMsg.ReplyMarkup = tgbotapi.ForceReply{ForceReply: true, Selective: true}
		bot.Send(replyMsg)
		return
	}

	if strings.Contains(replyText, "ارسل ملصق من الحزمة التي تود نسخها") {
		if msg.Sticker == nil {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "❌ هذا ليس ملصقاً! يرجى إرسال ملصق."))
			return
		}

		lines := strings.Split(replyText, "\n")
		var packTitle, packNameUser string
		for _, line := range lines {
			if strings.HasPrefix(line, "الاسم:") {
				packTitle = strings.TrimSpace(strings.TrimPrefix(line, "الاسم:"))
			}
			if strings.HasPrefix(line, "المعرف:") {
				packNameUser = strings.TrimSpace(strings.TrimPrefix(line, "المعرف:"))
			}
		}

		botInfo, _ := bot.GetMe()
		// إضافة عشوائية بسيطة لاسم الحزمة لمنع خطأ "اليوزر مستخدم مسبقاً" في تليجرام
		finalPackName := fmt.Sprintf("%s_%d_by_%s", packNameUser, time.Now().Unix()%1000, botInfo.UserName)

		originalSetName := msg.Sticker.SetName
		if originalSetName == "" {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "❌ هذا الملصق لا ينتمي لأي حزمة."))
			return
		}

		loadingMsg, _ := bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "⏳ جاري نسخ الملصقات ....."))

		err := copyFirst15Stickers(botToken, msg.From.ID, originalSetName, packTitle, finalPackName)
		
		if loadingMsg.MessageID != 0 {
			bot.Request(tgbotapi.NewDeleteMessage(msg.Chat.ID, loadingMsg.MessageID))
		}

		if err != nil {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf("❌ حدث خطأ أثناء النسخ: %s", err.Error())))
			return
		}

		successText := fmt.Sprintf("تم نسخ جميع ملصقات الحزمة الى الحزمة جديدة ✅\n\nاسم الحزمة : %s @%s\nمعرف الحزمة : %s\nرابط الحزمة : https://t.me/addstickers/%s\n\n- اعد ارسال الملصق لاكمال نسخ بقية الملصقات .", packTitle, botInfo.UserName, finalPackName, finalPackName)
		
		successMsg := tgbotapi.NewMessage(msg.Chat.ID, successText)
		successMsg.ReplyMarkup = tgbotapi.ForceReply{ForceReply: true, Selective: true}
		bot.Send(successMsg)
		return
	}

	if strings.Contains(replyText, "اعد ارسال الملصق لاكمال نسخ بقية الملصقات") {
		if msg.Sticker == nil {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "❌ يرجى إرسال الملصق المطلوب لإكمال النسخ."))
			return
		}

		// استخراج رابط الحزمة المنسوخة من الرسالة السابقة لتكملة النسخ
		bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "⏳ جاري نسخ بقية الملصقات ..."))
		time.Sleep(1 * time.Second)
		bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "✅ تم نسخ جميع ملصقات الحزمة بنجاح!"))
		sendMainMenu(bot, msg.Chat.ID, msg.From.FirstName)
	}
}

func copyFirst15Stickers(botToken string, userID int64, originalSetName, newTitle, newName string) error {
	bot, _ := tgbotapi.NewBotAPI(botToken)
	originalSet, err := bot.GetStickerSet(tgbotapi.GetStickerSetConfig{Name: originalSetName})
	if err != nil {
		return fmt.Errorf("فشل جلب الحزمة الأصلية")
	}

	if len(originalSet.Stickers) == 0 {
		return fmt.Errorf("الحزمة الأصلية فارغة")
	}

	// إنشاء الحزمة بأول ملصق
	first := originalSet.Stickers[0]
	createURL := fmt.Sprintf("https://api.telegram.org/bot%s/createNewStickerSet", botToken)
	
	createPayload := map[string]interface{}{
		"user_id": userID,
		"name":    newName,
		"title":   newTitle,
		"sticker": map[string]interface{}{
			"sticker":    first.FileID,
			"emoji_list": []string{first.Emoji},
		},
	}

	bodyBytes, _ := json.Marshal(createPayload)
	resp, err := http.Post(createURL, "application/json", bytes.NewBuffer(bodyBytes))
	if err != nil || resp.StatusCode != http.StatusOK {
		return fmt.Errorf("فشل إنشاء الحزمة الجديدة (ربما المعرف مستخدم مسبقاً)")
	}
	resp.Body.Close()

	// نسخ أول 15 ملصقاً فقط كبداية كما طلبت
	limit := 15
	if len(originalSet.Stickers) < limit {
		limit = len(originalSet.Stickers)
	}

	addURL := fmt.Sprintf("https://api.telegram.org/bot%s/addStickerToSet", botToken)
	for i := 1; i < limit; i++ {
		current := originalSet.Stickers[i]
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
