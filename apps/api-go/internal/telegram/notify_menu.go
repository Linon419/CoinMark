package telegram

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tele "gopkg.in/telebot.v3"
)

const notifyPrefCallbackUnique = "notify_pref_toggle"

func registerNotifyMenuHandlers(b *tele.Bot, notifier *AnomalyNotifier, manageChatID int64) {
	if b == nil || notifier == nil || manageChatID == 0 {
		return
	}

	_ = b.SetCommands([]tele.Command{
		{Text: "push", Description: "推送开关设置"},
	}, tele.CommandScope{Type: tele.CommandScopeAllGroupChats})

	showMenu := func(c tele.Context) error {
		if !notifyChatAllowed(c, manageChatID) {
			return c.Send("该命令仅在管理群可用。")
		}
		p := notifier.mustLoadPrefs(context.Background())
		text, markup := renderNotifyPrefsMessage(p)
		return c.Send(text, &tele.SendOptions{ReplyMarkup: markup})
	}

	b.Handle("/push", showMenu)
	b.Handle("/pushmenu", showMenu)

	cbHandler := func(c tele.Context) error {
		if !notifyChatAllowed(c, manageChatID) {
			_ = c.Respond()
			return nil
		}
		args := c.Args()
		if len(args) < 1 {
			_ = c.Respond()
			return nil
		}
		action := strings.ToLower(strings.TrimSpace(args[0]))
		p := notifier.mustLoadPrefs(context.Background())

		if action == "refresh" {
			text, markup := renderNotifyPrefsMessage(p)
			_ = c.Respond()
			return c.Edit(text, &tele.SendOptions{ReplyMarkup: markup})
		}
		if action != "toggle" || len(args) < 2 {
			_ = c.Respond()
			return nil
		}

		switch strings.ToLower(strings.TrimSpace(args[1])) {
		case "market":
			p.MarketAnomalyEnabled = !p.MarketAnomalyEnabled
		case "whale":
			p.WhaleWallEnabled = !p.WhaleWallEnabled
		case "absorption":
			p.AbsorptionEnabled = !p.AbsorptionEnabled
		case "mute":
			p.MuteAll = !p.MuteAll
		default:
			_ = c.Respond()
			return nil
		}
		_ = notifier.savePrefs(context.Background(), p)

		text, markup := renderNotifyPrefsMessage(p)
		_ = c.Respond()
		return c.Edit(text, &tele.SendOptions{ReplyMarkup: markup})
	}

	b.Handle("\f"+notifyPrefCallbackUnique, cbHandler)
	b.Handle(&tele.Btn{Unique: notifyPrefCallbackUnique}, cbHandler)
}

func notifyChatAllowed(c tele.Context, chatID int64) bool {
	ch := c.Chat()
	return ch != nil && ch.ID == chatID
}

func renderNotifyPrefsMessage(p tgNotifyPrefs) (string, *tele.ReplyMarkup) {
	mark := func(v bool) string {
		if v {
			return "[开]"
		}
		return "[关]"
	}

	text := strings.Join([]string{
		"【推送设置】",
		"当前配置作用于同一个目标频道/群。",
		fmt.Sprintf("目标 chat_id: `%d`", p.ChatID),
		fmt.Sprintf("%s 市场异动", mark(p.MarketAnomalyEnabled)),
		fmt.Sprintf("%s 大户挂单", mark(p.WhaleWallEnabled)),
		fmt.Sprintf("%s 吸筹", mark(p.AbsorptionEnabled)),
		fmt.Sprintf("%s 全部静音", mark(p.MuteAll)),
		"",
		"建议：只打开真正需要打断你的类型。",
	}, "\n")

	menu := &tele.ReplyMarkup{}
	btnMarket := menu.Data(mark(p.MarketAnomalyEnabled)+" 市场异动", notifyPrefCallbackUnique, "toggle", "market")
	btnWhale := menu.Data(mark(p.WhaleWallEnabled)+" 大户挂单", notifyPrefCallbackUnique, "toggle", "whale")
	btnAbsorption := menu.Data(mark(p.AbsorptionEnabled)+" 吸筹", notifyPrefCallbackUnique, "toggle", "absorption")
	btnMute := menu.Data(mark(p.MuteAll)+" 全部静音", notifyPrefCallbackUnique, "toggle", "mute")
	btnRefresh := menu.Data("刷新状态", notifyPrefCallbackUnique, "refresh", strconv.FormatInt(p.ChatID, 10))

	menu.Inline(
		menu.Row(btnMarket, btnWhale),
		menu.Row(btnAbsorption, btnMute),
		menu.Row(btnRefresh),
	)
	return text, menu
}
