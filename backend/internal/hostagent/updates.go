package hostagent

import (
	"context"
	"encoding/json"
	"fmt"
)

// VersionInfo — сведения о версии, установленной на сервере.
//
// Версию берём не из образа, а из каталога установки: там лежит git-коммит,
// с которого собраны работающие контейнеры. Так хеш всегда совпадает с тем,
// что реально запущено, и его не нужно вшивать в образ при сборке.
type VersionInfo struct {
	Dir     string `json:"dir"`
	SHA     string `json:"sha"`
	Short   string `json:"short"`
	Date    string `json:"date"`
	Subject string `json:"subject"`
	Branch  string `json:"branch"`
	Remote  string `json:"remote"`
	// Dirty — есть ли в каталоге незакоммиченные правки.
	Dirty bool `json:"dirty"`
}

// UpdateCommit — один коммит из списка изменений.
type UpdateCommit struct {
	SHA     string `json:"sha"`
	Short   string `json:"short"`
	Date    string `json:"date"`
	Subject string `json:"subject"`
}

// UpdateCheck — результат проверки обновлений.
type UpdateCheck struct {
	Dir     string       `json:"dir"`
	Branch  string       `json:"branch"`
	Current UpdateCommit `json:"current"`
	Remote  UpdateCommit `json:"remote"`
	// Behind — есть ли в репозитории коммиты новее установленных.
	Behind  bool     `json:"behind"`
	Commits []string `json:"commits"`
	// Diff — сводка изменений, например «5 files changed, 120 insertions(+)».
	Diff  string `json:"diff"`
	Dirty bool   `json:"dirty"`
	// Compose — найден ли на хосте docker compose: без него установка невозможна.
	Compose bool `json:"compose"`
	// FreeGB и EnoughSpace — место на диске: сборка образов требует запаса.
	FreeGB      float64 `json:"free_gb"`
	EnoughSpace bool    `json:"enough_space"`
	// Previous — коммит, с которого обновлялись в прошлый раз (для отката).
	Previous map[string]string `json:"previous"`
}

// UpdateState — текущее состояние установки обновления.
type UpdateState struct {
	// State: idle — ничего не идёт, running — идёт установка, done и failed — итог.
	State      string `json:"state"`
	Step       string `json:"step"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
	Error      string `json:"error"`
}

// UpdateStatus — состояние установки вместе с журналом.
type UpdateStatus struct {
	State UpdateState `json:"state"`
	Log   []string    `json:"log"`
}

// копирует набор полей из ответа агента в структуру.
//
// Ответ агента приходит как map: набор полей зависит от действия. Через
// промежуточный JSON получается один код разбора вместо ручного
// присваивания каждого поля.
func decode(raw map[string]any, target any) error {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, target)
}

// VersionInfo запрашивает у агента сведения о версии в каталоге установки.
func (c *Client) VersionInfo(ctx context.Context, dir string) (*VersionInfo, error) {
	payload := map[string]any{}
	if dir != "" {
		payload["dir"] = dir
	}

	raw, err := c.call(ctx, "version_info", payload)
	if err != nil {
		return nil, err
	}

	info := &VersionInfo{}
	if err := decode(raw, info); err != nil {
		return nil, fmt.Errorf("не удалось разобрать сведения о версии: %w", err)
	}
	return info, nil
}

// UpdateSource — откуда брать обновления: адрес репозитория, ветка и токен
// доступа. Собирается из настроек сервера, а не только из окружения:
// оператор меняет их на вкладке обновлений.
type UpdateSource struct {
	Repo   string
	Branch string
	// Token — токен для приватного репозитория. Пусто — репозиторий публичный.
	Token string
}

// UpdateCheck проверяет, есть ли в репозитории версия новее установленной.
//
// Обращение идёт в сеть, поэтому у действия отдельный, длинный таймаут —
// задан в call.
func (c *Client) UpdateCheck(ctx context.Context, dir string, src UpdateSource) (*UpdateCheck, error) {
	payload := map[string]any{}
	if dir != "" {
		payload["dir"] = dir
	}
	if src.Branch != "" {
		payload["branch"] = src.Branch
	}
	if src.Repo != "" {
		payload["repo"] = src.Repo
	}
	if src.Token != "" {
		payload["token"] = src.Token
	}

	raw, err := c.call(ctx, "update_check", payload)
	if err != nil {
		return nil, err
	}

	check := &UpdateCheck{}
	if err := decode(raw, check); err != nil {
		return nil, fmt.Errorf("не удалось разобрать результат проверки: %w", err)
	}
	return check, nil
}

// UpdateApply запускает установку обновления. Возвращается сразу: сама
// установка идёт на хосте минутами, а ход виден через UpdateStatus.
func (c *Client) UpdateApply(ctx context.Context, dir string, src UpdateSource) error {
	payload := map[string]any{}
	if dir != "" {
		payload["dir"] = dir
	}
	if src.Branch != "" {
		payload["branch"] = src.Branch
	}
	if src.Repo != "" {
		payload["repo"] = src.Repo
	}
	if src.Token != "" {
		payload["token"] = src.Token
	}

	_, err := c.call(ctx, "update_apply", payload)
	return err
}

// UpdateRollback возвращает прежнюю версию, сохранённую перед обновлением.
func (c *Client) UpdateRollback(ctx context.Context, dir string) error {
	payload := map[string]any{}
	if dir != "" {
		payload["dir"] = dir
	}

	_, err := c.call(ctx, "update_rollback", payload)
	return err
}

// UpdateStatus запрашивает состояние установки и хвост журнала.
func (c *Client) UpdateStatus(ctx context.Context, _ string) (*UpdateStatus, error) {
	raw, err := c.call(ctx, "update_status", nil)
	if err != nil {
		return nil, err
	}

	status := &UpdateStatus{}
	if err := decode(raw, status); err != nil {
		return nil, fmt.Errorf("не удалось разобрать состояние обновления: %w", err)
	}
	return status, nil
}
