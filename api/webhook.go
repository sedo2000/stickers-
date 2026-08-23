package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// نقطة البداية الأساسية للـ Webhook
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
		handleMessage(bot, update.Message)
	} else if update.CallbackQuery != nil {
		handleCallbackQuery(bot, update.CallbackQuery)
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "OK")
}

// معالجة الرسائل والأوامر
func handleMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) {
	if msg.IsCommand() && msg.Command() == "start" {
		keyboard := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("📦 نسخ حزمة ملصقات", "start_copy"),
			),
		)

		welcomeText := fmt.Sprintf("أهلاً بك يا %s! 👋\nأنا بوت متخصص في استنساخ حزم الملصقات ونقلها لتكون باسمك.\n\nاضغط على الزر بالأسفل للبدء:", msg.From.FirstName)
		reply := tgbotapi.NewMessage(msg.Chat.ID, welcomeText)
		reply.ReplyMarkup = keyboard

		bot.Send(reply)
		return
	}

	if msg.ReplyToMessage != nil {
		handleForceReply(bot, msg)
		return
	}
}

// معالجة الأزرار الشفافة (Inline Buttons)
func handleCallbackQuery(bot *tgbotapi.BotAPI, query *tgbotapi.CallbackQuery) {
	if query.Data == "start_copy" {
		text := "للبدء في إنشاء حزمتك، أرسل لي اسماً للحزمة واليوزر المطلوب مفصولين بشرطة (-).\n\nمثال:\nحزمتي الجديدة - mycoolpack"
		msg := tgbotapi.NewMessage(query.Message.Chat.ID, text)
		msg.ReplyMarkup = tgbotapi.ForceReply{ForceReply: true, Selective: true}
		bot.Send(msg)
		bot.Request(tgbotapi.NewCallback(query.ID, ""))
	}
}

// معالجة الردود الإجبارية (ForceReply) بخطواتها المرتبة
func handleForceReply(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) {
	// الخطوة 1: استقبال الاسم واليوزر
	if strings.Contains(msg.ReplyToMessage.Text, "أرسل لي اسماً للحزمة واليوزر") {
		parts := strings.Split(msg.Text, "-")
		if len(parts) != 2 {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "❌ الصيغة غير صحيحة. يرجى استخدام: الاسم - اليوزر"))
			return
		}

		packTitle := strings.TrimSpace(parts[0])
		packName := strings.TrimSpace(parts[1])
		
		nextStepText := fmt.Sprintf("ممتاز! لقد اخترت:\nالاسم: %s\nاليوزر: %s\n\nالآن، أرسل لي ملصقاً واحداً من الحزمة التي تريد نسخها.", packTitle, packName)
		
		replyMsg := tgbotapi.NewMessage(msg.Chat.ID, nextStepText)
		replyMsg.ReplyMarkup = tgbotapi.ForceReply{ForceReply: true, Selective: true}
		bot.Send(replyMsg)
		return
	}

	// الخطوة 2: استقبال الملصق وتنفيذ النسخ بشكل متزامن وآمن (بدون Goroutine لتجنب تجميد Vercel)
	if strings.Contains(msg.ReplyToMessage.Text, "أرسل لي ملصقاً واحداً") {
		if msg.Sticker == nil {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "❌ هذا ليس ملصقاً! يرجى إرسال ملصق من الحزمة."))
			return
		}

		lines := strings.Split(msg.ReplyToMessage.Text, "\n")
		var packTitle, userPackName string
		for _, line := range lines {
			if strings.HasPrefix(line, "الاسم:") {
				packTitle = strings.TrimSpace(strings.TrimPrefix(line, "الاسم:"))
			}
			if strings.HasPrefix(line, "اليوزر:") {
				userPackName = strings.TrimSpace(strings.TrimPrefix(line, "اليوزر:"))
			}
		}

		botInfo, _ := bot.GetMe()
		finalPackName := fmt.Sprintf("%s_by_%s", userPackName, botInfo.UserName)

		originalSetName := msg.Sticker.SetName
		if originalSetName == "" {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "❌ هذا الملصق لا ينتمي لأي حزمة."))
			return
		}

		loadingMsg, _ := bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "⏳ جاري استنساخ الحزمة بالكامل... يرجى الانتظار قليلاً."))

		err := copyStickerSetSync(bot, msg.Chat.ID, msg.From.ID, originalSetName, packTitle, finalPackName, msg.Sticker.Type)
		
		if loadingMsg.MessageID != 0 {
			bot.Request(tgbotapi.NewDeleteMessage(msg.Chat.ID, loadingMsg.MessageID))
		}

		if err != nil {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf("❌ حدث خطأ أثناء النسخ: %s", err.Error())))
			return
		}

		successText := fmt.Sprintf("✅ **تم استنساخ الحزمة بنجاح!** 🎉\n\nرابط حزمتك:\nt.me/addstickers/%s", finalPackName)
		successMsg := tgbotapi.NewMessage(msg.Chat.ID, successText)
		successMsg.ParseMode = "Markdown"
		bot.Send(successMsg)
	}
}

// دالة النسخ المتزامنة مع الحماية ضد حظر تيليجرام (Rate Limit)
func copyStickerSetSync(bot *tgbotapi.BotAPI, chatID int64, userID int64, originalSetName, newTitle, newName, stickerType string) error {
	stickerSetConfig := tgbotapi.GetStickerSetConfig{Name: originalSetName}
	originalSet, err := bot.GetStickerSet(stickerSetConfig)
	if err != nil {
		return fmt.Errorf("فشل جلب الحزمة الأصلية")
	}

	if len(originalSet.Stickers) == 0 {
		return fmt.Errorf("الحزمة الأصلية فارغة")
	}

	firstSticker := originalSet.Stickers[0]
	inputSticker := tgbotapi.InputSticker{
		Sticker:   firstSticker.FileID,
		EmojiList: []string{firstSticker.Emoji},
	}

	createConfig := tgbotapi.CreateNewStickerSetConfig{
		UserID:        userID,
		Name:          newName,
		Title:         newTitle,
		StickerFormat: stickerType,
		Stickers:      []tgbotapi.InputSticker{inputSticker},
	}

	_, err = bot.Request(createConfig)
	if err != nil {
		return fmt.Errorf("فشل إنشاء الحزمة (ربما اليوزر مستخدم مسبقاً)")
	}

	for i := 1; i < len(originalSet.Stickers); i++ {
		currentSticker := originalSet.Stickers[i]
		
		addConfig := tgbotapi.AddStickerToSetConfig{
			UserID: userID,
			Name:   newName,
			Sticker: tgbotapi.InputSticker{
				Sticker:   currentSticker.FileID,
				EmojiList: []string{currentSticker.Emoji},
			},
		}

		bot.Request(addConfig)
		time.Sleep(100 * time.Millisecond)
	}

	return nil
}
