/**
 * English interface text.
 *
 * Kept deliberately close to the Russian source in wording: the person
 * reading the English interface is usually the same person who set the
 * server up, and matching terms makes switching between languages painless.
 */

export default {
  nav: {
    dashboard: 'Dashboard',
    grid: 'Grid',
    cameras: 'Cameras',
    scanner: 'Scanner',
    events: 'Events',
    audioEvents: 'Sounds',
    recordings: 'Archive',
    logs: 'Logs',
    majestic: 'Streamer',
    recognition: 'Recognition',
    acs: 'Access control',
    plans: 'Floor plans',
    switches: 'Switches',
    access: 'Access',
    externalAccess: 'External access',
    notifications: 'Notifications',
    server: 'Server',
    settings: 'Settings',
    logout: 'Sign out',
  },

  common: {
    save: 'Save',
    saving: 'Saving…',
    saved: 'Saved',
    cancel: 'Cancel',
    close: 'Close',
    delete: 'Delete',
    edit: 'Edit',
    add: 'Add',
    create: 'Create',
    apply: 'Apply',
    refresh: 'Refresh',
    reload: 'Reload',
    search: 'Search',
    loading: 'Loading…',
    error: 'Error',
    retry: 'Retry',
    yes: 'Yes',
    no: 'No',
    enabled: 'Enabled',
    disabled: 'Disabled',
    enabledShort: 'on',
    disabledShort: 'off',
    unknown: 'Unknown',
    notSet: 'Not set',
    none: 'None',
    all: 'All',
    total: 'Total',
    name: 'Name',
    status: 'Status',
    actions: 'Actions',
    type: 'Type',
    time: 'Time',
    date: 'Date',
    address: 'Address',
    comment: 'Comment',
    optional: 'optional',
  },

  language: {
    title: 'Interface language',
    description:
      'The language of buttons, menus and labels in the browser. The choice is remembered in this browser, so different people on the same server can each have their own.',
    note: 'Messages the server sends by itself — to Telegram, MAX and by email — are still in Russian for now.',
  },

  serverPage: {
    title: 'Server',
    intro:
      'Time and network of this server. Changes are applied by a separate service on the host — the video server itself has no system privileges.',

    agentDownTitle: 'The server management service is not responding.',
    agentDownHint: 'Check that the nvr-agent service is installed and running on the server.',
    agentInstall: 'Install it on the server itself with',

    timeTitle: 'Time',
    timeCheck: 'Check',
    timeCheckHint: 'Check the service is reachable',
    timeSynced: 'Time is synchronised',
    timeNotSynced: 'Synchronisation not confirmed',
    timeService: 'service',
    timeServer: 'server',
    timezone: 'Time zone',
    timezoneHint:
      'Days in the archive are grouped and event times are shown in this zone. Changing it restarts the time service.',
    ntpServers: 'Time servers (NTP)',
    ntpPlaceholder: 'pool.ntp.org, time.google.com',
    ntpHint:
      'Comma separated. The server checks its clock against these addresses. Empty — the default servers are used.',
    serveTime: 'Serve time to cameras',
    serveTimeHint:
      'The server will answer time requests on the network. Needed when cameras take their time from here rather than the internet, for example when they have no way out.',
    syncDetails: 'Synchronisation details',
    saveTime: 'Save time',
    timeSaved: 'Time settings saved',
    timeSaveFailed: 'Could not save the time settings',

    netTitle: 'Network',
    netWarningTitle: 'Changing the address cuts the connection to the server.',
    netWarningText:
      'After applying, the browser will lose the connection and you will need to open it at the new address. If the new address turns out to be unreachable, the settings roll back on their own within a minute — the server checks the gateway for that.',
    iface: 'Network interface',
    ifaceHint: 'The interface the server is connected through. The list comes from the system.',
    addrMode: 'Address mode',
    dhcp: 'Automatic (DHCP)',
    dhcpHint: 'The router hands out the address. Simple and safe.',
    static: 'Permanent (static)',
    staticHint: 'The address is fixed to the server.',
    dns: 'Name servers (DNS)',
    dnsPlaceholder: '192.168.1.1, 8.8.8.8',
    dnsHint:
      'Comma separated. Needed for the server to find other hosts by name. Empty — the router\u2019s servers are used.',
    netFile: 'Current network settings file',
    applyNet: 'Apply network settings',
    rollbackHint: 'If the connection does not come back within a minute, the settings roll back on their own.',
    netConfirm:
      'Changing the network settings will cut the connection to the server for a short while.\n\nIf the new address turns out to be unreachable, the settings roll back automatically within a minute. Continue?',
    netApplied: 'Network settings applied',
    netFailed: 'Could not apply the network settings',

    statusLoadFailed: 'Could not get the server status',
    agentOk: 'The service on the server is responding',
    agentFail: 'The service on the server is not responding',
    loading: 'Loading the server settings…',
    addr: 'Address',
    mask: 'Netmask',
    gateway: 'Gateway',
    notAvailable: 'unavailable',
    notAvailableHint: 'While the service is unavailable you cannot change the settings — there would be nowhere to apply them.',
  },
}
