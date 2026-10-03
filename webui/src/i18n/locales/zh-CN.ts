/**
 * 简体中文界面文字。
 *
 * 术语尽量短：中文的按钮和菜单项占宽比俄文大，同一行里多两三个字就可能
 * 被截断，而侧边栏的截断最难辨认——旁边没有上下文可以猜。
 *
 * 「Архив」在各处含义不同，这里分开处理：作为录像存放处译作「录像库」，
 * 作为某个事件的录像译作「录像」。混用会让用户以为说的是两样东西。
 */

export default {
  nav: {
    dashboard: '总览',
    grid: '画面墙',
    cameras: '摄像头',
    scanner: '扫描',
    events: '事件',
    audioEvents: '声音',
    recordings: '录像库',
    logs: '日志',
    majestic: '推流器',
    recognition: '识别',
    acs: '门禁',
    plans: '平面图',
    switches: '交换机',
    access: '通行',
    externalAccess: '外部访问',
    notifications: '通知',
    server: '服务器',
    settings: '设置',
    logout: '退出',
  },

  common: {
    save: '保存',
    saving: '正在保存…',
    saved: '已保存',
    cancel: '取消',
    close: '关闭',
    delete: '删除',
    edit: '修改',
    add: '添加',
    create: '创建',
    apply: '应用',
    refresh: '刷新',
    reload: '重新加载',
    search: '搜索',
    loading: '正在加载…',
    error: '错误',
    retry: '重试',
    yes: '是',
    no: '否',
    enabled: '已启用',
    disabled: '已停用',
    enabledShort: '开',
    disabledShort: '关',
    unknown: '未知',
    notSet: '未设置',
    none: '无',
    all: '全部',
    total: '合计',
    name: '名称',
    status: '状态',
    actions: '操作',
    type: '类型',
    time: '时间',
    date: '日期',
    address: '地址',
    comment: '备注',
    optional: '选填',
  },

  language: {
    title: '界面语言',
    description:
      '按钮、菜单和标签在浏览器中的语言。选择只记在这个浏览器里，所以同一个服务器上不同的人可以各用各的语言。',
    note: '服务器自己发出的消息——发到 Telegram、MAX 和邮件的通知——目前仍是俄文。',
  },
}
