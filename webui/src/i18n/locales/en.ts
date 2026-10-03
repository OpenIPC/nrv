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
}
