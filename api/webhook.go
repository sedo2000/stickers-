package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// دالة Handler هي نقطة الدخول التي سيستدعيها Vercel لكل طلب
func Handler(w http.ResponseWriter, r *http.Request) {
	// 1. جلب توكن البوت من متغيرات البيئة في Vercel
	botToken := os.Getenv("BOT_TOKEN")
	if botToken == "" {
		http.Error(w, "Bot token not configured", http.StatusInternalServerError)
		return
	}

	// 2. تهيئة البوت
	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		http.Error(w, "Failed to create bot", http.StatusInternalServerError)
		return
	}

	// 3. قراءة البيانات القادمة من تيليجرام (Webhook)
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

	// 4. توجيه الأحداث (هل هي رسالة عادية أم ضغطة على زر؟)
	if update.Message != nil {
		handleMessage(bot, update.Message)
	} else if update.CallbackQuery != nil {
		handleCallbackQuery(bot, update.CallbackQuery)
	}

	// يجب دائماً الرد بـ 200 OK لكي لا يعيد تيليجرام إرسال نفس الطلب
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "OK")
}

// دالة للتعامل مع الرسائل النصية والأوامر
func handleMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) {
	// أمر /start
	if msg.IsCommand() && msg.Command() == "start" {
		// إنشاء الزر الشفاف
		keyboard := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("📦 نسخ حزمة ملصقات", "start_copy"),
			),
		)

		// صياغة رسالة الترحيب باستخدام اسم المستخدم
		welcomeText := fmt.Sprintf("أهلاً بك يا %s! 👋\nأنا بوت متخصص في استنساخ حزم الملصقات ونقلها لتكون باسمك.\n\nاضغط على الزر بالأسفل للبدء:", msg.From.FirstName)

		reply := tgbotapi.NewMessage(msg.Chat.ID, welcomeText)
		reply.ReplyMarkup = keyboard

		bot.Send(reply)
		return
	}

	// هنا سنتعامل مع الـ ForceReply لاحقاً (عندما يرسل المستخدم اسم الحزمة أو الملصق)
	if msg.ReplyToMessage != nil {
		handleForceReply(bot, msg)
		return
	}
}

// دالة للتعامل مع ضغطات الأزرار الشفافة
func handleCallbackQuery(bot *tgbotapi.BotAPI, query *tgbotapi.CallbackQuery) {
	// إذا ضغط المستخدم على زر "نسخ حزمة ملصقات"
	if query.Data == "start_copy" {
		text := "للبدء في إنشاء حزمتك، أرسل لي اسماً للحزمة واليوزر المطلوب مفصولين بشرطة (-).\n\nمثال:\nحزمتي الجديدة - mycoolpack"

		msg := tgbotapi.NewMessage(query.Message.Chat.ID, text)
		
		// استخدام ForceReply لإجبار المستخدم على الرد على هذه الرسالة
		msg.ReplyMarkup = tgbotapi.ForceReply{
			ForceReply: true,
			Selective:  true,
		}

		bot.Send(msg)

		// إرسال استجابة لتيليجرام لإخفاء علامة "التحميل" من الزر
		bot.Request(tgbotapi.NewCallback(query.ID, ""))
	}
}

// دالة للتعامل مع إجابات المستخدم على رسائل الـ ForceReply
func handleForceReply(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) {
	// التأكد أن المستخدم يرد على رسالة طلب اسم الحزمة
	if strings.Contains(msg.ReplyToMessage.Text, "أرسل لي اسماً للحزمة واليوزر") {
		// تفكيك النص المرسل بناءً على الشرطة (-)
		parts := strings.Split(msg.Text, "-")
		if len(parts) != 2 {
			errorMsg := tgbotapi.NewMessage(msg.Chat.ID, "❌ الصيغة غير صحيحة. يرجى إرسال الاسم واليوزر مفصولين بشرطة (-). مثال:\nاسم حزمتي - packusername")
			bot.Send(errorMsg)
			return
		}

		packTitle := strings.TrimSpace(parts[0])
		packName := strings.TrimSpace(parts[1])

		// هنا سننتقل للخطوة التالية (طلب ملصق من الحزمة المراد نسخها)
		// سنكملها في الجزء القادم!
		successMsg := tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf("✅ تم حفظ البيانات مؤقتاً:\nالاسم: %s\nاليوزر: %s\n\n(جاري إعداد الخطوة التالية...)", packTitle, packName))
		bot.Send(successMsg)
	}
}
