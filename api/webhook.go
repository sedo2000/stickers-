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

// تخزين مؤقت لجلسات العمليات والخطوات المتسلسلة
var userSessions = make(map[int64]*StickerPackSession)

// قاعدة بيانات وهمية مؤقتة لتخزين حزم المستخدمين (لتشغيل أزرار حزماتي وعرض الحزم)
var userPacksDB = make(map[int64][]string) // userID -> list of pack names

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
	if msg.IsCommand() && msg.Command() == "start" {
		sendHomeMenu(bot, msg.Chat.ID, msg.From.FirstName)
		return
	}

	// التحقق إذا كان المستخدم في منتصف عملية نسخ (بواسطة ForceReply)
	if msg.ReplyToMessage != nil {
		handleForceReplySteps(bot, msg, botToken)
		return
	}

	// إذا أرسل المستخدم رابط حزمة مباشرة لتحكم بها
	if strings.Contains(msg.Text, "t.me/addstickers/") {
		handleDirectPackLink(bot, msg)
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

func handleIncomingCallback(bot *tgbotapi.BotAPI, query *tgbotapi.CallbackQuery, botToken string) {
	chatId := query.Message.Chat.ID
	userId := query.From.ID
	data := query.Data

	bot.Request(tgbotapi.NewCallback(query.ID, ""))

	if data == "start_copy_flow" {
		userSessions[userId] = &StickerPackSession{Step: "awaiting_title"}
		
		text := "الان ارسل اسم الحزمة الذي تريده 🗣"
		msg := tgbotapi.NewMessage(chatId, text)
		msg.ReplyMarkup = tgbotapi.ForceReply{ForceReply: true, Selective: true}
		bot.Send(msg)

	} else if data == "my_packs_menu" {
		sendUserPacksList(bot, chatId, userId)

	} else if strings.HasPrefix(data, "manage_pack_") {
		packName := strings.TrimPrefix(data, "manage_pack_")
		sendPackControlPanel(bot, chatId, packName)

	} else if strings.HasPrefix(data, "delete_pack_confirm_") {
		packName := strings.TrimPrefix(data, "delete_pack_confirm_")
		sendDeleteConfirmation(bot, chatId, packName)

	} else if strings.HasPrefix(data, "do_delete_pack_") {
		packName := strings.TrimPrefix(data, "do_delete_pack_")
		removePackFromUser(userId, packName)
		
		editMsg := tgbotapi.NewEditMessageText(chatId, query.Message.MessageID, "🗑 تم حذف الحزمة من قائمتك بنجاح!")
		bot.Send(editMsg)
		sendHomeMenu(bot, chatId, query.From.FirstName)

	} else if strings.HasPrefix(data, "edit_title_") {
		packName := strings.TrimPrefix(data, "edit_title_")
		userSessions[userId] = &StickerPackSession{Step: "editing_title", Name: packName}

		msg := tgbotapi.NewMessage(chatId, "✏️ أرسل اسم الحزمة الجديد الآن:")
		msg.ReplyMarkup = tgbotapi.ForceReply{ForceReply: true, Selective: true}
		bot.Send(msg)

	} else if strings.HasPrefix(data, "del_sticker_") {
		packName := strings.TrimPrefix(data, "del_sticker_")
		userSessions[userId] = &StickerPackSession{Step: "deleting_sticker", Name: packName}

		msg := tgbotapi.NewMessage(chatId, "🗑 أرسل الملصق الذي تريد حذفه من هذه الحزمة:")
		msg.ReplyMarkup = tgbotapi.ForceReply{ForceReply: true, Selective: true}
		bot.Send(msg)
	}
}

func handleForceReplySteps(bot *tgbotapi.BotAPI, msg *tgbotapi.Message, botToken string) {
	userId := msg.From.ID
	session, exists := userSessions[userId]
	if !exists {
		// إذا لم تكن هناك جلسة نشطة، نبدأ من جديد لتفادي أخطاء الـ start
		sendHomeMenu(bot, msg.Chat.ID, msg.From.FirstName)
		return
	}

	replyText := msg.ReplyToMessage.Text

	// حالات التعديل الخاصة بالحزمة الموجودة مسبقاً
	if session.Step == "editing_title" {
		newTitle := strings.TrimSpace(msg.Text)
		// تنفيذ تغيير اسم الحزمة عبر API تليجرام
		err := updateStickerSetTitle(botToken, session.Name, newTitle)
		if err != nil {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "❌ فشل تغيير اسم الحزمة."))
		} else {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "✅ تم تغيير اسم الحزمة بنجاح!"))
		}
		delete(userSessions, userId)
		sendHomeMenu(bot, msg.Chat.ID, msg.From.FirstName)
		return
	}

	if session.Step == "deleting_sticker" {
		if msg.Sticker == nil {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "❌ يرجى إرسال ملصق صحيح لحذفه."))
			return
		}
		err := deleteStickerFromFile(botToken, msg.Sticker.FileID)
		if err != nil {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "❌ حدث خطأ أثناء حذف الملصق."))
		} else {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "✅ تم مسح الملصق من الحزمة بنجاح!"))
		}
		delete(userSessions, userId)
		sendHomeMenu(bot, msg.Chat.ID, msg.From.FirstName)
		return
	}

	// خطوات إنشاء ونسخ الحزمة الأساسية
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

		// حفظ الحزمة في قائمة المستخدم
		addUserPack(userId, finalPackName)

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

func handleDirectPackLink(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) {
	responseTxt := "🎛 تم رصد رابط الحزمة الخاصة بك.\nيمكنك التحكم بها من قسم (حزماتي) في القائمة الرئيسية."
	bot.Send(tgbotapi.NewMessage(msg.Chat.ID, responseTxt))
	sendHomeMenu(bot, msg.Chat.ID, msg.From.FirstName)
}

// إدارة قسم "حزماتي" وعرض الأزرار التفصيلية
func sendUserPacksList(bot *tgbotapi.BotAPI, chatId int64, userId int64) {
	packs := userPacksDB[userId]
	if len(packs) == 0 {
		bot.Send(tgbotapi.NewMessage(chatId, "📂 ليس لديك أي حزم مسجلة حتى الآن.\nقم بإنشاء أو نسخ حزمة جديدة أولاً."))
		sendHomeMenu(bot, chatId, "")
		return
	}

	var rows [][]tgbotapi.InlineKeyboardButton
	for _, packName := range packs {
		btn := tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("📦 %s", packName), "manage_pack_"+packName)
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(btn))
	}
	
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("🔙 رجوع", "back_home")))

	msg := tgbotapi.NewMessage(chatId, "📂 **حزماتي الخاصة بك:**\nاختر الحزمة التي تريد تعديلها أو التحكم بها:")
	msg.ReplyMarkup = tgbotapi.InlineKeyboardMarkup{InlineKeyboard: rows}
	bot.Send(msg)
}

func sendPackControlPanel(bot *tgbotapi.BotAPI, chatId int64, packName string) {
	text := fmt.Sprintf("⚙️ **لوحة تحكم الحزمة:**\n`%s`\n\nاختر الإجراء المناسب:", packName)
	
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("✏️ تعديل اسم الحزمة", "edit_title_"+packName),
			tgbotapi.NewInlineKeyboardButtonData("🗑 حذف ملصق", "del_sticker_"+packName),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("❌ مسح الحزمة بالكامل", "delete_pack_confirm_"+packName),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("🔗 رابط الحزمة: %s", packName), "url_dummy"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🔙 رجوع للحزم", "my_packs_menu"),
		),
	)

	msg := tgbotapi.NewMessage(chatId, text)
	msg.ParseMode = "Markdown"
	msg.ReplyMarkup = keyboard
	bot.Send(msg)
}

func sendDeleteConfirmation(bot *tgbotapi.BotAPI, chatId int64, packName string) {
	text := fmt.Sprintf("⚠️ **هل أنت متأكد من رغبتك في حذف الحزمة بالكامل؟**\n`%s`", packName)

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("✅ نعم، متأكد", "do_delete_pack_"+packName),
			tgbotapi.NewInlineKeyboardButtonData("❌ إلغاء", "manage_pack_"+packName),
		),
	)

	msg := tgbotapi.NewMessage(chatId, text)
	msg.ParseMode = "Markdown"
	msg.ReplyMarkup = keyboard
	bot.Send(msg)
}

// مساعدة الذاكرة المؤقتة للحزم
func addUserPack(userId int64, packName string) {
	for _, p := range userPacksDB[userId] {
		if p == packName {
			return
		}
	}
	userPacksDB[userId] = append(userPacksDB[userId], packName)
}

func removePackFromUser(userId int64, packName string) {
	var newList []string
	for _, p := range userPacksDB[userId] {
		if p != packName {
			newList = append(newList, p)
		}
	}
	userPacksDB[userId] = newList
}

func updateStickerSetTitle(botToken, name, title string) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/setStickerSetTitle?name=%s&title=%s", botToken, name, title)
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func deleteStickerFromFile(botToken, fileId string) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/deleteStickerFromSet?sticker=%s", botToken, fileId)
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
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
				FileID     string `json:"file_id"`
				Emoji      string `json:"emoji"`
				IsVideo    bool   `json:"is_video"`
				IsAnimated bool   `json:"is_animated"`
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
		
		stickerField := "png_sticker"
		if first.IsVideo {
			stickerField = "video_sticker"
		} else if first.IsAnimated {
			stickerField = "tgs_sticker"
		}

		createPayload := map[string]interface{}{
			"user_id":      userID,
			"name":         newName,
			"title":        newTitle,
			stickerField:   first.FileID,
			"emojis":       first.Emoji,
		}

		bodyBytes, _ := json.Marshal(createPayload)
		creResp, err := http.Post(createURL, "application/json", bytes.NewBuffer(bodyBytes))
		if err != nil || creResp.StatusCode != http.StatusOK {
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
