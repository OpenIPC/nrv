package hostagent

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// DetectionRestartResult — что перезапустил агент на хосте.
type DetectionRestartResult struct {
	// Dir — каталог установки, из которого запускался compose.
	Dir string `json:"dir"`
	// Services — перезапущенные сервисы.
	Services []string `json:"services"`
	// Output — хвост вывода compose. Нужен при отказе: по коду ответа
	// причина не видна, а «не удалось перезапустить» без подробностей
	// отправляет искать её в журнал службы на хосте.
	Output string `json:"output"`
}

// RestartDetection перезапускает контейнеры видеоаналитики.
//
// Почему через агента, а не из бэкенда. Бэкенд работает в контейнере и
// намеренно не имеет доступа ни к docker, ни к каталогу установки — иначе
// любая его уязвимость становилась бы уязвимостью всего сервера. Перезапуск
// выполняет агент: единственный процесс с правами root, который знает
// каталог установки.
//
// Почему перезапуск, а не перечитывание на ходу. Движки (YOLO, insightface,
// YAMNet) держат веса в памяти, а устройство вычислений выбирается при
// загрузке модели. Прочитать новое значение на ходу можно, но применить его
// без перезагрузки модели — нет, поэтому честнее перезапустить контейнер,
// чем показывать в интерфейсе настройку, которая не действует.
func (c *Client) RestartDetection(ctx context.Context, dir string, services []string) (*DetectionRestartResult, error) {
	payload := map[string]any{}
	if dir != "" {
		payload["dir"] = dir
	}
	if len(services) > 0 {
		payload["services"] = services
	}

	raw, err := c.call(ctx, "detection_restart", payload)
	if err != nil {
		// Служба на хосте обновляется вместе с установкой. На сервере,
		// который давно не обновляли, этого действия в ней ещё нет, и без
		// пояснения ответ «неизвестная команда» выглядит как ошибка в
		// запросе, а не как устаревшая служба.
		if strings.Contains(err.Error(), "неизвестная команда") {
			return nil, errors.New("служба управления хостом не умеет перезапускать видеоаналитику: " +
				"на сервере установлена её прежняя версия — обновите службу (nvr-agent) или перезапустите " +
				"контейнеры ai-detector и frame-publisher вручную")
		}
		return nil, err
	}

	out := &DetectionRestartResult{}
	if err := decode(raw, out); err != nil {
		return nil, fmt.Errorf("не удалось разобрать ответ о перезапуске детектора: %w", err)
	}
	return out, nil
}
