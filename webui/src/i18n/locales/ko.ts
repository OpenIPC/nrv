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
}
