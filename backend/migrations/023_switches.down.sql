-- Откат учёта PoE-коммутаторов.
--
-- Порядок обратный порядку создания: сначала журнал и привязки, потом
-- порты, потом сами коммутаторы. Ссылки объявлены с ON DELETE CASCADE,
-- но удалять родителя первым всё равно нельзя — сначала надо снять
-- зависимые строки, иначе на части версий PostgreSQL каскад сработает
-- раньше, чем мы успеем что-то сохранить для разбора.

DROP TABLE IF EXISTS switch_port_events;
DROP TABLE IF EXISTS camera_switch_port;
DROP TABLE IF EXISTS switch_ports;
DROP TABLE IF EXISTS switches;
