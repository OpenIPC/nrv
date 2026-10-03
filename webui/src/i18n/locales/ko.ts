/**
 * 한국어 인터페이스 텍스트.
 *
 * 표준어(대한민국 규범)를 따릅니다. 조선민주주의인민공화국에서 쓰는
 * 문화어는 같은 언어지만 어휘와 맞춤법, 띄어쓰기 규범이 다릅니다.
 * 특히 외래어를 거의 쓰지 않고 고유어로 바꾸어 쓰기 때문에, 표준어로
 * 옮긴 글은 북쪽 독자에게 낯설게 읽힙니다. 문화어 번역이 필요해지면
 * 이 파일을 별도 언어 코드(ko-KP)로 따로 두어야 하며, 기계 번역으로는
 * 만들 수 없습니다 — 표준어만 나오기 때문입니다.
 *
 * 용어는 짧게 유지합니다. 한국어는 같은 뜻이라도 음절 수가 많은 편이라
 * 좁은 사이드바에서 잘리기 쉽고, 잘린 메뉴 이름은 알아보기 어렵습니다.
 */

export default {
  nav: {
    dashboard: '대시보드',
    grid: '영상 격자',
    cameras: '카메라',
    scanner: '검색',
    events: '이벤트',
    audioEvents: '소리',
    recordings: '녹화 목록',
    logs: '로그',
    majestic: '스트리머',
    recognition: '인식',
    acs: '출입 통제',
    plans: '평면도',
    switches: '스위치',
    access: '출입',
    externalAccess: '외부 접속',
    notifications: '알림',
    server: '서버',
    settings: '설정',
    logout: '로그아웃',
  },

  common: {
    save: '저장',
    saving: '저장 중…',
    saved: '저장됨',
    cancel: '취소',
    close: '닫기',
    delete: '삭제',
    edit: '편집',
    add: '추가',
    create: '만들기',
    apply: '적용',
    refresh: '새로 고침',
    reload: '다시 불러오기',
    search: '검색',
    loading: '불러오는 중…',
    error: '오류',
    retry: '다시 시도',
    yes: '예',
    no: '아니요',
    enabled: '사용',
    disabled: '사용 안 함',
    enabledShort: '켬',
    disabledShort: '끔',
    unknown: '알 수 없음',
    notSet: '설정 안 됨',
    none: '없음',
    all: '전체',
    total: '합계',
    name: '이름',
    status: '상태',
    actions: '동작',
    type: '종류',
    time: '시간',
    date: '날짜',
    address: '주소',
    comment: '비고',
    optional: '선택 사항',
  },

  language: {
    title: '인터페이스 언어',
    description:
      '브라우저에서 버튼과 메뉴, 이름표에 쓰이는 언어입니다. 선택은 이 브라우저에만 저장되므로 같은 서버를 쓰는 사람마다 언어가 달라도 됩니다.',
    note: '서버가 스스로 보내는 메시지 — Telegram, MAX, 메일 알림 — 은 아직 러시아어입니다.',
  },

  serverPage: {
    title: '서버',
    intro: '이 서버의 시간과 네트워크입니다. 변경은 호스트의 별도 서비스가 수행합니다 — 영상 서버 자체에는 시스템 권한이 없습니다.',

    agentDownTitle: '서버 관리 서비스가 응답하지 않습니다.',
    agentDownHint: '서버에 nvr-agent 서비스가 설치되어 실행 중인지 확인하십시오.',
    agentInstall: '서버에서 다음 명령으로 설치합니다',

    timeTitle: '시간',
    timeCheck: '확인',
    timeCheckHint: '서비스 연결 확인',
    timeSynced: '시간이 동기화되었습니다',
    timeNotSynced: '동기화가 확인되지 않았습니다',
    timeService: '서비스',
    timeServer: '서버',
    timezone: '시간대',
    timezoneHint: '녹화 목록의 날짜 구분과 이벤트 시각이 이 시간대를 따릅니다. 시간대를 바꾸면 시간 서비스가 재시작됩니다.',
    ntpServers: '시간 서버 (NTP)',
    ntpPlaceholder: 'pool.ntp.org, time.google.com',
    ntpHint: '쉼표로 구분합니다. 서버는 이 주소들과 시계를 맞춥니다. 비우면 기본 서버를 씁니다.',
    serveTime: '카메라에 시간 제공',
    serveTimeHint:
      '서버가 네트워크의 시간 요청에 응답합니다. 카메라가 인터넷이 아니라 여기서 시간을 받는 경우에 필요합니다 — 외부 접속이 막힌 경우가 그렇습니다.',
    syncDetails: '동기화 상세',
    saveTime: '시간 저장',
    timeSaved: '시간 설정을 저장했습니다',
    timeSaveFailed: '시간 설정을 저장하지 못했습니다',

    netTitle: '네트워크',
    netWarningTitle: '주소를 바꾸면 서버 연결이 끊깁니다.',
    netWarningText:
      '적용하면 브라우저 연결이 끊기고 새 주소로 다시 열어야 합니다. 새 주소에 접속할 수 없으면 설정은 1분 안에 스스로 되돌아갑니다 — 서버가 그동안 게이트웨이 연결을 확인합니다.',
    iface: '네트워크 인터페이스',
    ifaceHint: '서버가 연결된 인터페이스입니다. 목록은 시스템에서 가져옵니다.',
    addrMode: '주소 방식',
    dhcp: '자동 (DHCP)',
    dhcpHint: '공유기가 주소를 할당합니다. 간편하고 안전합니다.',
    static: '고정 (정적)',
    staticHint: '주소가 서버에 고정됩니다.',
    dns: '이름 서버 (DNS)',
    dnsPlaceholder: '192.168.1.1, 8.8.8.8',
    dnsHint: '쉼표로 구분합니다. 서버가 다른 호스트를 이름으로 찾는 데 필요합니다. 비우면 공유기가 알려준 서버를 씁니다.',
    netFile: '현재 네트워크 설정 파일',
    applyNet: '네트워크 설정 적용',
    rollbackHint: '적용 후 1분 안에 연결이 돌아오지 않으면 설정이 스스로 되돌아갑니다.',
    netConfirm:
      '네트워크 설정을 바꾸면 잠시 서버 연결이 끊깁니다.\n\n새 주소에 접속할 수 없으면 설정은 1분 안에 자동으로 되돌아갑니다. 계속하시겠습니까?',
    netApplied: '네트워크 설정을 적용했습니다',
    netFailed: '네트워크 설정을 적용하지 못했습니다',

    statusLoadFailed: '서버 상태를 가져오지 못했습니다',
    agentOk: '서버의 서비스가 응답합니다',
    agentFail: '서버의 서비스가 응답하지 않습니다',
    loading: '서버 설정을 불러오는 중…',
    addr: '주소',
    mask: '마스크',
    gateway: '게이트웨이',
    notAvailable: '사용할 수 없음',
    notAvailableHint: '서비스를 사용할 수 없는 동안에는 설정을 변경할 수 없습니다. 변경 사항을 적용할 곳이 없기 때문입니다.',
  },
}
