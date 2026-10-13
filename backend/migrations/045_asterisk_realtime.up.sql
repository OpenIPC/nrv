-- Схема Asterisk Realtime: абоненты и диалплан лежат в нашей базе.
--
-- Почему таблицы выглядят чужими, а имена колонок длинные: это официальная
-- схема Asterisk (contrib/ast-db-manage, ветка 20). Колонки называются так
-- же, как параметры pjsip.conf: Asterisk сам читает из них настройки и сам
-- пишет туда контакты зарегистрированных устройств. Переименовать их нельзя —
-- соответствие «объект pjsip ↔ таблица» задаётся в sorcery.conf, и любая
-- вольность превратится в параметр, который молча не применился.
--
-- Что изменено против официальной схемы:
--   * колонки-перечисления (ast_bool_values и прочие pjsip_*_values) объявлены
--     как text. Ограничение значений нам ничего не даёт — эти строки пишем мы
--     сами, — а стоит дорого: при обновлении Asterisk список допустимых
--     значений расширяется, и старая таблица перестала бы их принимать;
--   * appdata расширен с varchar(256) до text: в диалплане встречаются длинные
--     строки («Set(CALLLIST=PJSIP/114&PJSIP/116&…)»), и varchar(256) обрезал
--     бы вызов молча;
--   * callerid расширен с varchar(40) до text. Это имя абонента в формате
--     «"Вызывная панель Калитка" <101>». 40 символов хватает не всегда,
--     а переполнение тут — не мелочь: вставка падает с ошибкой, и абонент
--     пропадает из конфигурации целиком.
--
-- Наши таблицы (sip_accounts, sip_groups, sip_group_members, sip_rules)
-- остаются главными: с ними работает оператор через интерфейс. Таблицы ниже
-- заполняет сервер из них — в этом и смысл: Asterisk читает настройки прямо
-- из базы, и правка абонента вступает в силу без перезагрузки конфигурации.
CREATE TABLE extensions (
    id bigint NOT NULL,
    context character varying(40) NOT NULL,
    exten character varying(40) NOT NULL,
    priority integer NOT NULL,
    app character varying(40) NOT NULL,
    appdata text NOT NULL
);
CREATE SEQUENCE extensions_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;
ALTER SEQUENCE extensions_id_seq OWNED BY extensions.id;
-- Без этой строки вставка правила диалплана без явного id падала бы на
-- NOT NULL: последовательность создана, но не привязана к колонке.
ALTER TABLE ONLY extensions ALTER COLUMN id SET DEFAULT nextval('extensions_id_seq'::regclass);
CREATE TABLE ps_aors (
    id character varying(255) NOT NULL,
    contact character varying(255),
    default_expiration integer,
    mailboxes character varying(80),
    max_contacts integer,
    minimum_expiration integer,
    remove_existing text,
    qualify_frequency integer,
    authenticate_qualify text,
    maximum_expiration integer,
    outbound_proxy character varying(255),
    support_path text,
    qualify_timeout double precision,
    voicemail_extension character varying(40),
    remove_unavailable text,
    qualify_2xx_only text
);
CREATE TABLE ps_auths (
    id character varying(255) NOT NULL,
    auth_type text,
    nonce_lifetime integer,
    md5_cred character varying(40),
    password character varying(80),
    realm character varying(255),
    username character varying(40),
    refresh_token character varying(255),
    oauth_clientid character varying(255),
    oauth_secret character varying(255),
    password_digest character varying(1024),
    supported_algorithms_uas character varying(1024),
    supported_algorithms_uac character varying(1024)
);
CREATE TABLE ps_contacts (
    id character varying(255) NOT NULL,
    uri character varying(511),
    expiration_time bigint,
    qualify_frequency integer,
    outbound_proxy character varying(255),
    path text,
    user_agent character varying(255),
    qualify_timeout double precision,
    reg_server character varying(255),
    authenticate_qualify text,
    via_addr character varying(40),
    via_port integer,
    call_id character varying(255),
    endpoint character varying(255),
    prune_on_boot text,
    qualify_2xx_only text
);
CREATE TABLE ps_endpoints (
    id character varying(255) NOT NULL,
    transport character varying(40),
    aors character varying(2048),
    auth character varying(255),
    context character varying(40),
    disallow character varying(200),
    allow character varying(200),
    direct_media text,
    connected_line_method text,
    direct_media_method text,
    direct_media_glare_mitigation text,
    disable_direct_media_on_nat text,
    dtmf_mode text,
    external_media_address character varying(40),
    force_rport text,
    ice_support text,
    identify_by character varying(80),
    mailboxes character varying(40),
    moh_suggest character varying(40),
    outbound_auth character varying(255),
    outbound_proxy character varying(255),
    rewrite_contact text,
    rtp_ipv6 text,
    rtp_symmetric text,
    send_diversion text,
    send_pai text,
    send_rpid text,
    timers_min_se integer,
    timers text,
    timers_sess_expires integer,
    callerid text,
    callerid_privacy text,
    callerid_tag character varying(40),
    "100rel" text,
    aggregate_mwi text,
    trust_id_inbound text,
    trust_id_outbound text,
    use_ptime text,
    use_avpf text,
    media_encryption text,
    inband_progress text,
    call_group character varying(40),
    pickup_group character varying(40),
    named_call_group character varying(40),
    named_pickup_group character varying(40),
    device_state_busy_at integer,
    fax_detect text,
    t38_udptl text,
    t38_udptl_ec text,
    t38_udptl_maxdatagram integer,
    t38_udptl_nat text,
    t38_udptl_ipv6 text,
    tone_zone character varying(40),
    language character varying(40),
    one_touch_recording text,
    record_on_feature character varying(40),
    record_off_feature character varying(40),
    rtp_engine character varying(40),
    allow_transfer text,
    allow_subscribe text,
    sdp_owner character varying(40),
    sdp_session character varying(40),
    tos_audio character varying(10),
    tos_video character varying(10),
    sub_min_expiry integer,
    from_domain character varying(40),
    from_user character varying(40),
    mwi_from_user character varying(40),
    dtls_verify character varying(40),
    dtls_rekey character varying(40),
    dtls_cert_file character varying(200),
    dtls_private_key character varying(200),
    dtls_cipher character varying(200),
    dtls_ca_file character varying(200),
    dtls_ca_path character varying(200),
    dtls_setup text,
    srtp_tag_32 text,
    media_address character varying(40),
    redirect_method text,
    set_var text,
    cos_audio integer,
    cos_video integer,
    message_context character varying(40),
    force_avp text,
    media_use_received_transport text,
    accountcode character varying(80),
    user_eq_phone text,
    moh_passthrough text,
    media_encryption_optimistic text,
    rpid_immediate text,
    g726_non_standard text,
    rtp_keepalive integer,
    rtp_timeout integer,
    rtp_timeout_hold integer,
    bind_rtp_to_media_address text,
    voicemail_extension character varying(40),
    mwi_subscribe_replaces_unsolicited text,
    deny character varying(95),
    permit character varying(95),
    acl character varying(40),
    contact_deny character varying(95),
    contact_permit character varying(95),
    contact_acl character varying(40),
    subscribe_context character varying(40),
    fax_detect_timeout integer,
    contact_user character varying(80),
    preferred_codec_only text,
    asymmetric_rtp_codec text,
    rtcp_mux text,
    allow_overlap text,
    refer_blind_progress text,
    notify_early_inuse_ringing text,
    max_audio_streams integer,
    max_video_streams integer,
    webrtc text,
    dtls_fingerprint text,
    incoming_mwi_mailbox character varying(40),
    bundle text,
    dtls_auto_generate_cert text,
    follow_early_media_fork text,
    accept_multiple_sdp_answers text,
    suppress_q850_reason_headers text,
    trust_connected_line text,
    send_connected_line text,
    ignore_183_without_sdp text,
    codec_prefs_incoming_offer character varying(128),
    codec_prefs_outgoing_offer character varying(128),
    codec_prefs_incoming_answer character varying(128),
    codec_prefs_outgoing_answer character varying(128),
    stir_shaken text,
    send_history_info text,
    allow_unauthenticated_options text,
    t38_bind_udptl_to_media_address text,
    geoloc_incoming_call_profile character varying(80),
    geoloc_outgoing_call_profile character varying(80),
    incoming_call_offer_pref text,
    outgoing_call_offer_pref text,
    stir_shaken_profile character varying(80),
    security_negotiation text,
    security_mechanisms character varying(512),
    send_aoc text,
    overlap_context character varying(80),
    tenantid character varying(80),
    suppress_moh_on_sendonly text,
    follow_redirect_methods character varying(95),
    rtp_port_start integer,
    rtp_port_end integer
);
ALTER TABLE ONLY extensions
    ADD CONSTRAINT extensions_context_exten_priority_key UNIQUE (context, exten, priority);
ALTER TABLE ONLY extensions
    ADD CONSTRAINT extensions_pkey PRIMARY KEY (id);
ALTER TABLE ONLY ps_aors
    ADD CONSTRAINT ps_aors_id_key UNIQUE (id);
ALTER TABLE ONLY ps_auths
    ADD CONSTRAINT ps_auths_id_key UNIQUE (id);
ALTER TABLE ONLY ps_contacts
    ADD CONSTRAINT ps_contacts_id_key UNIQUE (id);
ALTER TABLE ONLY ps_contacts
    ADD CONSTRAINT ps_contacts_uq UNIQUE (id, reg_server);
ALTER TABLE ONLY ps_endpoints
    ADD CONSTRAINT ps_endpoints_id_key UNIQUE (id);
CREATE INDEX ps_aors_id ON ps_aors USING btree (id);
CREATE INDEX ps_aors_qualifyfreq_contact ON ps_aors USING btree (qualify_frequency, contact);
CREATE INDEX ps_auths_id ON ps_auths USING btree (id);
CREATE INDEX ps_contacts_id ON ps_contacts USING btree (id);
CREATE INDEX ps_contacts_qualifyfreq_exp ON ps_contacts USING btree (qualify_frequency, expiration_time);
CREATE INDEX ps_endpoints_id ON ps_endpoints USING btree (id);
