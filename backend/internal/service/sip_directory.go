package service

import (
	"context"
	"sync"
	"time"

	"github.com/nvr/backend/internal/repository/postgres"
)

// SipDirectory — справочник номеров домофонии: кто звонил и куда сообщать.
//
// Зачем кэш. Справочник нужен на каждое событие вызова, а события приходят
// пачками (на один вызов — по событию на каждую трубку в группе). Читать
// список абонентов из базы каждый раз означало бы десяток запросов на один
// звонок. Список меняется только руками оператора, поэтому короткого кэша
// достаточно: правки видны почти сразу, а база не нагружается.
type SipDirectory struct {
	repo *postgres.SipRepo

	mu       sync.Mutex
	entries  map[string]SipDirectoryEntry
	loadedAt time.Time
	// ttl — срок жизни кэша. Минуты: этого хватает, чтобы не бить в базу
	// на каждом событии, и мало, чтобы правки «зависали».
	ttl time.Duration
}

// NewSipDirectory создаёт справочник поверх репозитория домофонии.
func NewSipDirectory(repo *postgres.SipRepo) *SipDirectory {
	return &SipDirectory{
		repo:    repo,
		entries: map[string]SipDirectoryEntry{},
		ttl:     time.Minute,
	}
}

// Resolve находит номер среди абонентов и групп.
//
// Группы тоже нужны: панель может звонить не на устройство, а в группу
// обзвона, и в журнале это должно выглядеть как «звонок в группу Все
// устройства», а не как звонок на непонятный номер 200.
func (d *SipDirectory) Resolve(ctx context.Context, number string) (SipDirectoryEntry, bool) {
	if number == "" {
		return SipDirectoryEntry{}, false
	}
	d.refreshIfStale(ctx)

	d.mu.Lock()
	defer d.mu.Unlock()

	entry, ok := d.entries[number]
	return entry, ok
}

// refreshIfStale обновляет кэш, если он устарел.
func (d *SipDirectory) refreshIfStale(ctx context.Context) {
	d.mu.Lock()
	fresh := time.Since(d.loadedAt) < d.ttl && len(d.entries) > 0
	d.mu.Unlock()
	if fresh {
		return
	}
	d.reload(ctx)
}

// reload перечитывает абонентов и группы.
func (d *SipDirectory) reload(ctx context.Context) {
	entries := map[string]SipDirectoryEntry{}

	accounts, err := d.repo.ListAccounts(ctx)
	if err != nil {
		// Ошибку чтения не показываем как сбой обработки вызова: без
		// справочника вызов просто не будет записан с именем. Кэш не
		// обновляем, чтобы попробовать снова на следующем событии.
		return
	}
	for i := range accounts {
		account := accounts[i]
		if account.Number == "" {
			continue
		}
		entry := SipDirectoryEntry{
			Number: account.Number,
			Name:   account.Name,
			// Каналы берём вместе с общей галочкой «уведомлять о
			// пропущенных»: без неё сообщать не о чем, а решать это
			// в двух местах — значит однажды разойтись.
			NotifyTelegram: account.NotifyTelegram && account.NotifyMissed,
			NotifyMax:      account.NotifyMax && account.NotifyMissed,
			NotifyMissed:   account.NotifyMissed,
			RecordMissed:   account.RecordMissed,
			CameraID:       account.CameraID,
		}
		id := account.ID
		entry.AccountID = &id
		entries[account.Number] = entry
	}

	groups, err := d.repo.ListGroups(ctx)
	if err != nil {
		return
	}
	for i := range groups {
		group := groups[i]
		if group.Number == "" {
			continue
		}
		// Группа не перекрывает абонента с тем же номером: у устройства
		// номер конкретнее, и путать их нельзя.
		if _, exists := entries[group.Number]; exists {
			continue
		}
		entries[group.Number] = SipDirectoryEntry{Number: group.Number, Name: group.Name}
	}

	d.mu.Lock()
	d.entries = entries
	d.loadedAt = time.Now()
	d.mu.Unlock()
}
