package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type StickerPackSession struct {
	Title      string
	Name       string
	Original   string
	Step       string
	PackType   string // "png", "video", "animated"
	AllFileIDs []string
	AllEmojis  []string
	CurrentIdx int
}

var userSessions = make(map[int64]*StickerPackSession)
var userPacksDB = make(map[int64][]string)

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
		// مسح أي جلسة قديمة لتبدأ من نظيف
		delete(userSessions, msg.From.ID)
		sendHomeMenu(bot, msg.Chat.ID, msg.From.FirstName)
		return
	}

	// معالجة جلسات الخطوات النشطة للمستخدم بص بغض النظر عن الـ ReplyToMessage إذا كان في خطوة نسخ
	userId := msg.From.ID
	session, exists := userSessions[userId]
	if exists {
		handleActiveSession(bot, msg, botToken, session)
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
		sendHomeMenu(bot, msg.Chat.ID, msg.From.FirstName)
		return
	}

	replyText := msg.ReplyToMessage.Text

	if session.Step == "editing_title" {
		newTitle := strings.TrimSpace(msg.Text)
		updateStickerSetTitle(botToken, session.Name, newTitle)
		bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "✅ تم تغيير اسم الحزمة بنجاح!"))
		delete(userSessions, userId)
		sendHomeMenu(bot, msg.Chat.ID, msg.From.FirstName)
		return
	}

	if session.Step == "deleting_sticker" {
		if msg.Sticker == nil {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "❌ يرجى إرسال ملصق صحيح لحذفه."))
			return
		}
		deleteStickerFromFile(botToken, msg.Sticker.FileID)
		bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "✅ تم مسح الملصق من الحزمة بنجاح!"))
		delete(userSessions, userId)
		sendHomeMenu(bot, msg.Chat.ID, msg.From.FirstName)
		return
	}

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
}

// معالجة الجلسة النشطة مباشرة لتجنب ضياع الملصقات عند استكمال النسخ
func handleActiveSession(bot *tgbotapi.BotAPI, msg *tgbotapi.Message, botToken string, session *StickerPackSession) {
	userId := msg.From.ID

	if session.Step == "awaiting_sticker" {
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
		session.Name = finalPackName

		loadingMsg, _ := bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "⏳ جاري جلب وقراءة كل الملصقات (بما فيها المتحركة والفيديو)..."))

		// جلب جميع الملصقات من الحزمة الأصلية وتخزينها في الجلسة
		fileIDs, emojis, packType, err := fetchAllStickersFromSet(botToken, originalSetName)
		
		if loadingMsg.MessageID != 0 {
			bot.Request(tgbotapi.NewDeleteMessage(msg.Chat.ID, loadingMsg.MessageID))
		}

		if err != nil || len(fileIDs) == 0 {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "❌ فشل قراءة الحزمة الأصلية تأكد من الرابط."))
			delete(userSessions, userId)
			return
		}

		session.AllFileIDs = fileIDs
		session.AllEmojis = emojis
		session.PackType = packType
		session.CurrentIdx = 0

		// إنشاء الحزمة بالملصق الأول
		loadingCreate, _ := bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "⏳ جاري إنشاء الحزمة الجديدة..."))
		err = createNewPackWithFirstSticker(botToken, userId, finalPackName, session.Title, session.PackType, session.AllFileIDs[0], session.AllEmojis[0])
		
		if loadingCreate.MessageID != 0 {
			bot.Request(tgbotapi.NewDeleteMessage(msg.Chat.ID, loadingCreate.MessageID))
		}

		if err != nil {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf("❌ خطأ إنشاء الحزمة: %s", err.Error())))
			delete(userSessions, userId)
			return
		}

		session.CurrentIdx = 1
		addUserPack(userId, finalPackName)

		// إذا كانت الحزمة قصيرة (أقل من أو تساوي 15 ملصقاً)، نكملها فوراً دفعة واحدة
		if len(session.AllFileIDs) <= 15 {
			executeRemainingStickers(botToken, userId, session.Name, session.PackType, session.AllFileIDs, session.AllEmojis, 1)
			
			finalDoneText := fmt.Sprintf("🎉 **تم الانتهاء من نسخ الحزمة بالكامل وإضافة جميع الملصقات بنجاح!**\n\nرابط الحزمة:\nhttps://t.me/addstickers/%s", session.Name)
			doneMsg := tgbotapi.NewMessage(msg.Chat.ID, finalDoneText)
			doneMsg.ParseMode = "Markdown"
			bot.Send(doneMsg)

			delete(userSessions, userId)
			sendHomeMenu(bot, msg.Chat.ID, msg.From.FirstName)
			return
		}

		// إذا كانت طويلة، ننسخ أول 15 ملصقاً أولاً ثم نطلب الملصق التالي للاستكمال
		executeRemainingStickers(botToken, userId, session.Name, session.PackType, session.AllFileIDs, session.AllEmojis, 1)
		session.CurrentIdx = 16 // وصلنا حتى 16

		successText := fmt.Sprintf("تم نسخ أول 15 ملصقاً بنجاح ✅\n\nاسم الحزمة: %s\nرابط الحزمة: https://t.me/addstickers/%s\n\n- أعد إرسال أي ملصق من الحزمة لإكمال نسخ باقي الملصقات تلقائياً.", session.Title, finalPackName)
		session.Step = "awaiting_completion"

		outMsg := tgbotapi.NewMessage(msg.Chat.ID, successText)
		outMsg.ReplyMarkup = tgbotapi.ForceReply{ForceReply: true, Selective: true}
		bot.Send(outMsg)
		return
	}

	if session.Step == "awaiting_completion" {
		if msg.Sticker == nil {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "❌ يرجى إرسال الملصق المطلوب لإتمام استكمال الحزمة."))
			return
		}

		loadingMsg, _ := bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "⏳ جاري إكمال نسخ باقي الملصقات..."))

		// نسخ الباقي حتى النهاية
		executeRemainingStickers(botToken, userId, session.Name, session.PackType, session.AllFileIDs, session.AllEmojis, session.CurrentIdx)

		if loadingMsg.MessageID != 0 {
			bot.Request(tgbotapi.NewDeleteMessage(msg.Chat.ID, loadingMsg.MessageID))
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

// جلب جميع الملصقات مع تحديد النوع بدقة (فيديو، متحركة تليجرام tgs، أو صور png)
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

	// للحزم المتحركة tgs يجب تحميل الملف أولاً أو تمرير الـ file_id بشكل صحيح عبر multipart إذا لزم، 
	// لكن لتليجرام يمكن تمرير الـ file_id مباشرة إذا كان نوع الملف متوافقاً
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

func executeRemainingStickers(botToken string, userID int64, newName, packType string, fileIDs []string, emojis []string, startIndex int) {
	addURL := fmt.Sprintf("https://api.telegram.org/bot%s/addStickerToSet", botToken)
	
	endIndex := startIndex + 15
	if endIndex > len(fileIDs) {
		endIndex = len(fileIDs)
	}

	for i := startIndex; i < endIndex; i++ {
		currentFileID := fileIDs[i]
		currentEmoji := emojis[i]

		addPayload := map[string]interface{}{
			"user_id": userID,
			"name":    newName,
			"sticker": map[string]interface{}{
				"sticker":    currentFileID,
				"emoji_list": []string{currentEmoji},
			},
		}

		addBytes, _ := json.Marshal(addPayload)
		addResp, err := http.Post(addURL, "application/json", bytes.NewBuffer(addBytes))
		if err == nil {
			addResp.Body.Close()
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// وظائف التحكم بالحزم الخاصة بقسم حزماتي
func sendUserPacksList(bot *tgbotapi.BotAPI, chatId int64, userId int64) {
	packs := userPacksDB[userId]
	if len(packs) == 0 {
		bot.Send(tgbotapi.NewMessage(chatId, "📂 ليس لديك أي حزم مسجلة في القائمة.\nقم بإنشاء أو نسخ حزمة جديدة أولاً."))
		sendHomeMenu(bot, chatId, "")
		return
	}

	var rows [][]tgbotapi.InlineKeyboardButton
	for _, packName := range packs {
		btn := tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("📦 %s", packName), "manage_pack_"+packName)
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(btn))
	}
	
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("🔙 رجوع", "back_home")))

	msg := tgbotapi.NewMessage(chatId, "📂 **حزماتي الخاصة بك:**\nاختر الحزمة التي تريد التحكم بها:")
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
	if err == nil {
		resp.Body.Close()
	}
	return nil
}

func deleteStickerFromFile(botToken, fileId string) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/deleteStickerFromSet?sticker=%s", botToken, fileId)
	resp, err := http.Get(url)
	if err == nil {
		resp.Body.Close()
	}
	return nil
}
