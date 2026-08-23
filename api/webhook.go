package handler

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

func Handler(w http.ResponseWriter, r *http.Request) {
	botToken := os.Getenv("BOT_TOKEN")
	if botToken == "" {
		http.Error(w, "Bot token not configured", http.StatusInternalServerError)
		return
	}

	// تحقق اختياري من صحة مصدر الطلب (فعّله بضبط متغير TELEGRAM_WEBHOOK_SECRET)
	if secret := os.Getenv("TELEGRAM_WEBHOOK_SECRET"); secret != "" {
		if r.Header.Get("X-Telegram-Bot-Api-Secret-Token") != secret {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
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
	if err := json.Unmarshal(body, &update); err != nil {
		http.Error(w, "Failed to unmarshal update", http.StatusBadRequest)
		return
	}

	// معالجة متزامنة (بدون goroutine) لأن Vercel توقف التنفيذ بمجرد إرجاع الرد
	if update.Message != nil {
		handleMessage(bot, update.Message)
	} else if update.CallbackQuery != nil {
		handleCallbackQuery(bot, update.CallbackQuery)
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "OK")
}

func handleMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) {
	if msg.IsCommand() && msg.Command() == "start" {
		keyboard := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("📦 نسخ حزمة ملصقات", "start_copy"),
			),
		)

		welcomeText := fmt.Sprintf(
			"أهلاً بك يا %s! 👋\nأنا بوت متخصص في استنساخ حزم الملصقات ونقلها لتكون باسمك.\n\nاضغط على الزر بالأسفل للبدء:",
			msg.From.FirstName,
		)
		reply := tgbotapi.NewMessage(msg.Chat.ID, welcomeText)
		reply.ReplyMarkup = keyboard
		bot.Send(reply)
		return
	}

	if msg.ReplyToMessage != nil {
		handleForceReply(bot, msg)
	}
}

func handleCallbackQuery(bot *tgbotapi.BotAPI, query *tgbotapi.CallbackQuery) {
	switch query.Data {
	case "start_copy":
		text := "للبدء في إنشاء حزمتك، أرسل لي اسماً للحزمة واليوزر المطلوب مفصولين بشرطة (-).\n\nمثال:\nحزمتي الجديدة - mycoolpack"
		msg := tgbotapi.NewMessage(query.Message.Chat.ID, text)
		msg.ReplyMarkup = tgbotapi.ForceReply{ForceReply: true, Selective: true}
		bot.Send(msg)
		bot.Request(tgbotapi.NewCallback(query.ID, ""))

	case "delete_sticker":
		text := "أرسل لي الملصق الذي تريد حذفه من حزمتك (يجب أن يكون ملصقاً من حزمة أنشأتها هذا البوت)."
		msg := tgbotapi.NewMessage(query.Message.Chat.ID, text)
		msg.ReplyMarkup = tgbotapi.ForceReply{ForceReply: true, Selective: true}
		bot.Send(msg)
		bot.Request(tgbotapi.NewCallback(query.ID, ""))
	}
}

func handleForceReply(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) {
	replyText := msg.ReplyToMessage.Text

	// الحالة 1: استلام الاسم واليوزر
	if strings.Contains(replyText, "أرسل لي اسماً للحزمة واليوزر") {
		parts := strings.Split(msg.Text, "-")
		if len(parts) != 2 {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "❌ الصيغة غير صحيحة. يرجى استخدام: الاسم - اليوزر"))
			return
		}

		packTitle := strings.TrimSpace(parts[0])
		packName := strings.TrimSpace(parts[1])

		nextStepText := fmt.Sprintf(
			"ممتاز! لقد اخترت:\nالاسم: %s\nاليوزر: %s\n\nالآن، أرسل لي ملصقاً واحداً من الحزمة التي تريد نسخها.",
			packTitle, packName,
		)

		replyMsg := tgbotapi.NewMessage(msg.Chat.ID, nextStepText)
		replyMsg.ReplyMarkup = tgbotapi.ForceReply{ForceReply: true, Selective: true}
		bot.Send(replyMsg)
		return
	}

	// الحالة 2: استلام ملصق من الحزمة المراد نسخها
	if strings.Contains(replyText, "أرسل لي ملصقاً واحداً") {
		if msg.Sticker == nil {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "❌ هذا ليس ملصقاً! يرجى إرسال ملصق من الحزمة."))
			return
		}

		lines := strings.Split(replyText, "\n")
		var packTitle, userPackName string
		for _, line := range lines {
			if strings.HasPrefix(line, "الاسم:") {
				packTitle = strings.TrimSpace(strings.TrimPrefix(line, "الاسم:"))
			}
			if strings.HasPrefix(line, "اليوزر:") {
				userPackName = strings.TrimSpace(strings.TrimPrefix(line, "اليوزر:"))
			}
		}

		botInfo, err := bot.GetMe()
		if err != nil {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "❌ تعذر التحقق من بيانات البوت، حاول مرة أخرى."))
			return
		}
		finalPackName := fmt.Sprintf("%s_by_%s", userPackName, botInfo.UserName)

		originalSetName := msg.Sticker.SetName
		if originalSetName == "" {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "❌ هذا الملصق لا ينتمي لأي حزمة."))
			return
		}

		bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "⏳ جاري استنساخ الحزمة... يرجى الانتظار."))
		copyStickerSet(bot, msg.Chat.ID, msg.From.ID, originalSetName, packTitle, finalPackName, msg.Sticker.Type)
		return
	}

	// الحالة 3: استلام الملصق المراد حذفه
	if strings.Contains(replyText, "أرسل لي الملصق الذي تريد حذفه") {
		if msg.Sticker == nil {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "❌ هذا ليس ملصقاً! يرجى إرسال الملصق المراد حذفه."))
			return
		}

		_, err := bot.Request(tgbotapi.DeleteStickerFromSetConfig{
			Sticker: msg.Sticker.FileID,
		})
		if err != nil {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "❌ فشل حذف الملصق. تأكد أن الملصق ينتمي لحزمة أنشأها هذا البوت."))
			return
		}

		bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "✅ تم حذف الملصق من الحزمة بنجاح."))
	}
}

func copyStickerSet(bot *tgbotapi.BotAPI, chatID int64, userID int64, originalSetName, newTitle, newName, stickerType string) {
	originalSet, err := bot.GetStickerSet(tgbotapi.GetStickerSetConfig{Name: originalSetName})
	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ حدث خطأ أثناء جلب الحزمة الأصلية."))
		return
	}

	if len(originalSet.Stickers) == 0 {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ الحزمة الأصلية فارغة."))
		return
	}

	firstSticker := originalSet.Stickers[0]
	createConfig := tgbotapi.CreateNewStickerSetConfig{
		UserID:        userID,
		Name:          newName,
		Title:         newTitle,
		StickerFormat: stickerType,
		Stickers: []tgbotapi.InputSticker{
			{Sticker: firstSticker.FileID, EmojiList: []string{firstSticker.Emoji}},
		},
	}

	if _, err = bot.Request(createConfig); err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("❌ فشل إنشاء الحزمة. قد يكون اليوزر (%s) مستخدماً مسبقاً.", newName)))
		return
	}

	failedCount := 0
	for i := 1; i < len(originalSet.Stickers); i++ {
		s := originalSet.Stickers[i]
		addConfig := tgbotapi.AddStickerToSetConfig{
			UserID:  userID,
			Name:    newName,
			Sticker: tgbotapi.InputSticker{Sticker: s.FileID, EmojiList: []string{s.Emoji}},
		}
		if _, err := bot.Request(addConfig); err != nil {
			failedCount++
		}
		time.Sleep(50 * time.Millisecond)
	}

	successText := fmt.Sprintf("✅ **تم استنساخ الحزمة بنجاح!** 🎉\n\nرابط حزمتك:\nt.me/addstickers/%s", newName)
	if failedCount > 0 {
		successText += fmt.Sprintf("\n\n⚠️ تعذّر نسخ %d ملصق من أصل %d.", failedCount, len(originalSet.Stickers)-1)
	}

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🗑️ حذف ملصق من حزمتي", "delete_sticker"),
		),
	)

	successMsg := tgbotapi.NewMessage(chatID, successText)
	successMsg.ReplyMarkup = keyboard
	successMsg.ParseMode = "Markdown"
	bot.Send(successMsg)
}
