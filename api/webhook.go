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
const TargetBotUsername = "Cut88bot" // الصيغة المطلوبة بالشُرطات السفلية

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

		nextMsg := tgbotapi.NewMessage(msg.Chat.ID, "📦 ممتاز! الآن أرسل ملصقاً من الحزمة التي تود نسخها (يدعم العادية، المتحركة، والمرئية):")
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

		// الالتزام بالصيغة المطلوبة بالشُرطات السفلية
		finalPackName := fmt.Sprintf("p%d_by_%s", time.Now().UnixNano()%1000000, TargetBotUsername)
		session.CreatedPackName = finalPackName
		session.Step = "processing"

		go processAndCopyAllStickersWithTransparentButton(bot, botToken, userId, session)
		return
	}
}

func processAndCopyAllStickersWithTransparentButton(bot *tgbotapi.BotAPI, botToken string, userId int64, session *StickerPackSession) {
	total := session.TotalCount
	
	// رسالة العداد بالزر الشفاف الأولية
	initialText := "📦 **جاري معالجة ورفع الحزمة (عادية، متحركة، مرئية)...**"
	initialKeyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("🔄 Batches: 1/%d (0%%)", total), "loading_status"),
		),
	)

	msgConfig := tgbotapi.NewMessage(session.ChatID, initialText)
	msgConfig.ParseMode = "Markdown"
	msgConfig.ReplyMarkup = initialKeyboard
	statusMsg, err := bot.Send(msgConfig)
	var statusMsgID int
	if err == nil {
		statusMsgID = statusMsg.MessageID
	}

	// 1. إنشاء الحزمة بالملصق الأول مع دعم النوع المناسب
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

	// 2. نظام الدفعات المتوازية السريعة
	batchSize := 5
	for i := 1; i < total; i += batchSize {
		end := i + batchSize
		if end > total {
			end = total
		}

		var wg sync.WaitGroup
		for j := i; j < end; j++ {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()

				addPayload := map[string]interface{}{
					"user_id": userId,
					"name":    session.CreatedPackName,
					"sticker": map[string]interface{}{
						"sticker":    session.AllFileIDs[index],
						"emoji_list": []string{session.AllEmojis[index]},
					},
				}

				addBytes, _ := json.Marshal(addPayload)
				resp, err := http.Post(addURL, "application/json", bytes.NewBuffer(addBytes))
				if err == nil {
					resp.Body.Close()
				}
			}(j)
		}
		wg.Wait()

		processed := end
		percent := (processed * 100) / total

		// تحديث الزر الشفاف بالتقدم الحقيقي
		if statusMsgID != 0 {
			btnText := fmt.Sprintf("🔄 Batches: %d/%d (%d%%)", processed, total, percent)
			if processed == total {
				btnText = fmt.Sprintf("✨ اكتملت الحزمة بنجاح! (%d/%d - 100%)", processed, total)
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

	// 3. حذف رسالة العداد الشفاف فوراً عند الاكتمال 100%
	if statusMsgID != 0 {
		bot.Request(tgbotapi.NewDeleteMessage(session.ChatID, statusMsgID))
	}

	// 4. إرسال رابط الحزمة النهائي تلقائياً وفوراً
	doneText := fmt.Sprintf("🎉 **تم نسخ الحزمة بالكامل بنجاح!**\n\n🏷 عنوان الحزمة: `%s`\n🔗 رابط الحزمة:\nhttps://t.me/addstickers/%s", session.UserCustomTitle, session.CreatedPackName)
	doneMsg := tgbotapi.NewMessage(session.ChatID, doneText)
	doneMsg.ParseMode = "Markdown"
	bot.Send(doneMsg)

	// إنهاء الجلسة وإرسال القائمة الرئيسية
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
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📁 حزماتي (إدارة الحزم)", "my_packs"),
		),
	)

	welcomeText := "أهلاً بك! 👋\nأنا بوت مخصص لـ **نسخ وإدارة حزم الملصقات (العادية، المتحركة، والمرئية)**.\n\nاختر ما يناسبك أدناه:"
	if firstName != "" {
		welcomeText = fmt.Sprintf("أهلاً بك يا %s! 👋\nأنا بوت مخصص لـ **نسخ وإدارة حزم الملصقات (العادية، المتحركة، والمرئية)**.\n\nاختر ما يناسبك أدناه:", firstName)
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
		callbackConfig := tgbotapi.NewCallback(query.ID, "🔄 العمليات جارية بأقصى سرعة، انتظر لحظات حتى تصل 100%!")
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
		return
	}

	// قسم حزماتي للإدارة الداخلية
	if data == "my_packs" {
		packsKeyboard := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("✏️ تعديل عنوان حزمة", "edit_title"),
				tgbotapi.NewInlineKeyboardButtonData("🗑 حذف ملصق من حزمة", "delete_sticker"),
			),
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("🔙 رجوع للقائمة الرئيسية", "back_home"),
			),
		)
		msg := tgbotapi.NewMessage(chatId, "📁 **قسم حزماتي (الإدارة الداخلية):**\nاختر العملية التي تريد تنفيذها على حزمتك:")
		msg.ParseMode = "Markdown"
		msg.ReplyMarkup = packsKeyboard
		bot.Send(msg)
		return
	}

	if data == "edit_title" {
		msg := tgbotapi.NewMessage(chatId, "✏️ لتعديل عنوان الحزمة من داخل البوت، أرسل اسم الحزمة أو رابطها متبوعاً بالعنوان الجديد.")
		msg.ParseMode = "Markdown"
		bot.Send(msg)
		return
	}

	if data == "delete_sticker" {
		msg := tgbotapi.NewMessage(chatId, "🗑 لحذف ملصق من حزمتك، أرسل الملصق المراد حذفه مباشرة إلى البوت هنا وسنقوم بمعالجته فوراً.")
		msg.ParseMode = "Markdown"
		bot.Send(msg)
		return
	}

	if data == "back_home" {
		sendHomeMenu(bot, chatId, "")
		return
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
		packType = "animated" // دعم الملصقات المتحركة TGS
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
		stickerField = "tgs_sticker" // دعم إنشاء الحزم المتحركة
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
