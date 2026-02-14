package telegram

import (
	"context"
	"log"
	"strconv"
	"time"

	tele "gopkg.in/telebot.v3"

	"coinmark/api-go/internal/binance"
	"coinmark/api-go/internal/config"
	chrepo "coinmark/api-go/internal/repo/ch"
	redisrepo "coinmark/api-go/internal/repo/redis"
	"coinmark/api-go/internal/repo/sqlite"
)

func Start(ctx context.Context, cfg *config.Config, store *sqlite.Store, ch *chrepo.Client, bn *binance.Client, redis *redisrepo.Store, stopCh <-chan struct{}) {
	if !cfg.TGEnabled {
		log.Println("tg: disabled")
		return
	}

	// Notify bot
	if cfg.TGNotifyBotToken != "" && cfg.TGNotifyChatID != "" {
		go startNotifyBot(ctx, cfg, store, redis, stopCh)
	}

	// Query bot
	if cfg.TGQueryBotToken != "" {
		go startQueryBot(ctx, cfg, store, ch, bn, redis, stopCh)
	}
}

func startNotifyBot(ctx context.Context, cfg *config.Config, store *sqlite.Store, redis *redisrepo.Store, stopCh <-chan struct{}) {
	pref := tele.Settings{
		Token:  cfg.TGNotifyBotToken,
		Poller: &tele.LongPoller{Timeout: 10 * time.Second},
	}
	b, err := tele.NewBot(pref)
	if err != nil {
		log.Printf("tg notify: init error: %v", err)
		return
	}

	chatIDInt, err := strconv.ParseInt(cfg.TGNotifyChatID, 10, 64)
	if err != nil {
		log.Printf("tg notify: invalid chat_id: %v", err)
		return
	}
	chat := &tele.Chat{ID: chatIDInt}
	adminChatIDRaw := cfg.TGNotifyAdminChatID
	if adminChatIDRaw == "" {
		adminChatIDRaw = cfg.TGNotifyChatID
	}
	adminChatIDInt, err := strconv.ParseInt(adminChatIDRaw, 10, 64)
	if err != nil {
		log.Printf("tg notify: invalid admin chat_id: %v", err)
		return
	}

	notifier := NewAnomalyNotifier(
		store, redis,
		cfg.TGNotifyChatID, cfg.TGNotifyMarket, cfg.TGNotifyMinLevel,
		cfg.TGStateRedisPrefix,
		cfg.TGNotifyPollIntervalSec, cfg.TGNotifyBatchWindowSec, cfg.TGNotifyBatchMaxItems,
	)
	notifier.chatIDInt = chatIDInt
	registerNotifyMenuHandlers(b, notifier, adminChatIDInt)

	sendFn := func(text string) error {
		_, err := b.Send(chat, text)
		return err
	}

	log.Println("tg notify: started")
	go notifier.RunLoop(ctx, sendFn, stopCh)
	go func() {
		select {
		case <-stopCh:
			b.Stop()
		case <-ctx.Done():
			b.Stop()
		}
	}()
	b.Start()
}

func startQueryBot(ctx context.Context, cfg *config.Config, store *sqlite.Store, ch *chrepo.Client, bn *binance.Client, redis *redisrepo.Store, stopCh <-chan struct{}) {
	qb, err := NewQueryBot(
		cfg.TGQueryBotToken, cfg.TGQueryPollTimeoutSec,
		store, ch, bn, redis, cfg.TGStateRedisPrefix,
	)
	if err != nil {
		log.Printf("tg query: init error: %v", err)
		return
	}

	log.Println("tg query: started")
	go func() {
		select {
		case <-stopCh:
			qb.Stop()
		case <-ctx.Done():
			qb.Stop()
		}
	}()
	qb.Start()
}
