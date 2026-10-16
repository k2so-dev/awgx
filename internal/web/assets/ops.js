const SYS_TABS = [{ id: 'general', t: 'awgx & updates', i: 'i-box' }, { id: 'access', t: 'Web access', i: 'i-key' }, { id: 'backups', t: 'Backups', i: 'i-archive' }, { id: 'setup', t: 'Setup wizard', i: 'i-wand' }]
const SETUP_PICKS = [
  { id: 'check', t: 'System check', d: 'Apply fixes and remove old WireGuard setups' },
  { id: 'endpoint', t: 'Endpoint', d: 'The address clients connect to' },
  { id: 'ipv6', t: 'IPv6', d: 'IPv6 addresses inside the tunnel' },
  { id: 'clients', t: 'Add clients', d: 'Create several clients at once' },
  { id: 'egress', t: 'Egress', d: 'Where client traffic leaves the server' }
]
const SETUP_TITLES = { check: 'System check', endpoint: 'Endpoint', ipv6: 'IPv6', clients: 'Clients', egress: 'Egress', review: 'Review' }

AWGX.push(() => ({
  doctorBusy: false,
  okOpen: false,
  fixSel: [],
  sys: { version: '', kernel: '', mode: '', modules: {}, update_check: false, update: '', web: {}, tunnel: '', backups: [], backup_dir: '', keep_backups: 0 },
  sysTabs: SYS_TABS,
  updBusy: false,
  pw: { current: '', password: '', repeat: '', totp: 'keep', secret: '', url: '', qr: '', code: '', busy: false, error: '', field: '' },
  su: { phase: 'intro', data: null, pick: [], fixes: [], legacy: [], bkOld: false, endpoint: '', ipv6: false, clients: '', egress: '', conf: '', token: '', ks: false, idx: 0, summary: [], result: null, error: '' },
  setupPicks: SETUP_PICKS,

  async load_doctor() { if (!this.doctor.ran_at) await this.runDoctor(true); else this.doctor = await this.api('GET', '/api/doctor?refresh=1') },
  async runDoctor(quiet) {
    this.doctorBusy = true
    try { this.doctor = await this.api(quiet === true ? 'GET' : 'POST', '/api/doctor'); this.fixSel = this.fixSel.filter(id => this.fixable.some(c => c.id === id)) } catch (e) { this.toast('fail', e.message) } finally { this.doctorBusy = false }
  },
  get dChecks() { return this.doctor.checks || [] },
  get dCounts() { const c = { ok: 0, warn: 0, fail: 0 }; for (const d of this.dChecks) c[d.status]++; return c },
  get dProblems() { return this.dChecks.filter(d => d.status !== 'ok').sort((a, b) => (a.status === 'fail' ? 0 : 1) - (b.status === 'fail' ? 0 : 1)) },
  get dOk() { return this.dChecks.filter(d => d.status === 'ok') },
  get hasProblems() { return this.dProblems.length > 0 },
  get fixable() { return this.dChecks.filter(d => d.fix && d.status !== 'ok') },
  get hasFixable() { return this.fixable.length > 0 },
  get doctorLoaded() { return !!this.doctor.ran_at },
  get doctorWhen() { this.now; return this.doctor.ran_at ? 'Last run ' + fmtRel(this.doctor.ran_at) : 'Running checks…' },
  get doctorHeadline() {
    const c = this.dCounts
    if (c.fail) return plural(c.fail, 'check fails', 'checks fail')
    if (c.warn) return 'Working, with ' + plural(c.warn, 'warning', 'warnings')
    return 'All checks pass'
  },
  get doctorIcon() { const c = this.dCounts; return c.fail ? 'i-x-circle' : c.warn ? 'i-alert' : 'i-check-circle' },
  get doctorIconClass() { const c = this.dCounts; return 'sum-ico ' + (c.fail ? 'fail-text' : c.warn ? 'warn-text' : 'ok-text') },
  get doctorBtn() { return this.doctorBusy ? 'Running…' : 'Run again' },
  get doctorSpin() { return this.doctorBusy ? 'icon spin' : 'icon' },
  checkIcon(d) { return d.status === 'fail' ? 'i-x-circle' : d.status === 'warn' ? 'i-alert' : 'i-check-circle' },
  checkClass(d) { return 'icon ' + (d.status === 'fail' ? 'fail-text' : d.status === 'warn' ? 'warn-text' : 'ok-text') },
  hasFix(d) { return !!d.fix },
  fixDesc(d) { return d.fix ? d.fix.description : '' },
  fixChecked(d) { return this.fixSel.includes(d.id) ? 'true' : 'false' },
  fixFlip(d) { const i = this.fixSel.indexOf(d.id); if (i < 0) this.fixSel.push(d.id); else this.fixSel.splice(i, 1) },
  fixLabel(d) { return 'Select fix for ' + d.id },
  get fixAll() { return this.hasFixable && this.fixable.every(d => this.fixSel.includes(d.id)) ? 'true' : 'false' },
  fixFlipAll() { this.fixSel = this.fixAll === 'true' ? [] : this.fixable.map(d => d.id) },
  get fixBar() { return this.fixSel.length > 0 },
  get fixBarText() { return plural(this.fixSel.length, 'fix', 'fixes') + ' selected' },
  clearFixes() { this.fixSel = [] },
  async runFixes(ids) {
    try {
      await this.startJob(this.api('POST', '/api/doctor/fix', { ids }), function () { this.fixSel = []; this.runDoctor(true) })
    } catch (e) { this.toast('fail', e.message) }
  },
  fixOne(d) { this.runFixes([d.id]) },
  fixSelected() { this.runFixes(this.fixSel.slice()) },
  get okToggleLabel() { return this.okOpen ? 'Hide' : 'Show' },
  get okExpanded() { return this.okOpen ? 'true' : 'false' },
  toggleOk() { this.okOpen = !this.okOpen },

  async load_system() { this.sys = await this.api('GET', '/api/system') },
  get sysTab() { return SYS_TABS.some(t => t.id === this.sub) ? this.sub : 'general' },
  sysTabIs(t) { return this.sysTab === t },
  sysTabCur(t) { return this.sysTab === t.id ? 'page' : false },
  sysTabHref(t) { return '#/system/' + t.id },
  get sysRows() {
    const s = this.sys
    return [{ k: 'awgx', v: s.version, c: 'mono' }, { k: 'Kernel', v: s.kernel, c: 'mono' }, { k: 'Mode', v: s.mode, c: '' }]
      .concat(Object.keys(s.modules || {}).map(k => ({ k, v: s.modules[k], c: 'mono break' })))
  },
  get hasUpdate() { return !!this.sys.update },
  get updateText() { return this.sys.update ? 'awgx ' + this.sys.update + ' is available. You run ' + this.sys.version + '.' : 'You run the latest version, ' + this.sys.version + '.' },
  get updateCheckOn() { return this.sys.update_check ? 'true' : 'false' },
  get metricsOn() { return this.sys.web.metrics ? 'true' : 'false' },
  async setSetting(k) {
    const body = {}
    body[k] = k === 'metrics' ? !this.sys.web.metrics : !this.sys.update_check
    try { await this.api('PUT', '/api/system/settings', body); await this.load_system(); this.toast('ok', 'Setting saved') } catch (e) { this.toast('fail', e.message) }
  },
  flipUpdateCheck() { this.setSetting('update_check') },
  flipMetrics() { this.setSetting('metrics') },
  async checkUpdate() {
    this.updBusy = true
    try {
      const r = await this.api('POST', '/api/system/update/check')
      this.toast('ok', r.latest && r.latest !== r.current ? 'awgx ' + r.latest + ' is available' : 'awgx is up to date')
      await this.refresh(true)
    } catch (e) { this.toast('fail', e.message) } finally { this.updBusy = false }
  },
  askUpdateSelf() {
    this.ask({
      title: 'Update awgx to ' + this.sys.update + '?', text: 'The new binary is downloaded and verified, then the data plane and the server restart. Open connections of peers drop and reconnect. You sign in again afterwards.', label: 'Update and restart',
      async run() { await this.startJob(this.api('POST', '/api/system/update/self'), function (j) { if (!j.error && j.result && j.result.restart) this.waitRestart('Updating awgx', '-u awgx-dataplane -u awgx-serve') }) }
    })
  },
  askUpgradeModules() {
    this.ask({
      title: 'Upgrade the kernel module and tools?', text: 'amneziawg-dkms and amneziawg-tools are upgraded and every instance restarts. Clients reconnect in a few seconds. On failure the old packages are restored.', label: 'Upgrade',
      async run() { await this.startJob(this.api('POST', '/api/system/update/modules')) }
    })
  },
  get web() { return this.sys.web || {} },
  get webUrl() { const w = this.web; return w.host ? 'https://' + w.host + '/' : 'http://' + (w.listen || '127.0.0.1:7457') + '/' },
  get webRows() {
    const w = this.web
    return [{ k: 'Listen address', v: w.listen, c: 'mono' }, { k: 'Public host', v: w.host || 'none, open through an SSH tunnel', c: w.host ? 'mono' : '' }, { k: 'Two-factor (TOTP)', v: w.totp ? 'on' : 'off', c: '' }]
  },
  copyTunnel() { this.copy(this.sys.tunnel, 'Command') },
  copyUrl() { this.copy(this.webUrl, 'URL') },
  get totpState() { return this.web.totp ? 'On. Codes are asked at sign-in.' : 'Off. Only the password is asked at sign-in.' },
  get totpBadge() { return this.web.totp ? 'badge badge-ok' : 'badge badge-muted' },
  get totpBadgeText() { return this.web.totp ? 'TOTP on' : 'TOTP off' },
  openPassword() {
    this.pw = { current: '', password: '', repeat: '', totp: 'keep', secret: '', url: '', qr: '', code: '', busy: false, error: '', field: '' }
    this.openDialog('pwDlg')
  },
  get totpOpts() { return this.web.totp ? [{ v: 'keep', t: 'Keep' }, { v: 'new', t: 'New device' }, { v: 'off', t: 'Turn off' }] : [{ v: 'keep', t: 'Keep off' }, { v: 'new', t: 'Set up' }] },
  totpChecked(o) { return this.pw.totp === o.v ? 'true' : 'false' },
  async setTotp(o) {
    this.pw.totp = o.v
    if (o.v === 'new' && !this.pw.secret) {
      try { const r = await this.api('POST', '/api/system/totp'); this.pw.secret = r.secret; this.pw.url = r.url; this.pw.qr = r.qr } catch (e) { this.pw.error = e.message }
    }
  },
  get pwNew() { return this.pw.totp === 'new' && !!this.pw.secret },
  get pwOff() { return this.pw.totp === 'off' },
  pwErr(f) { return this.pw.field === f ? this.pw.error : '' },
  pwInvalid(f) { return this.pw.field === f ? 'true' : 'false' },
  get pwGeneralError() { return this.pw.field ? '' : this.pw.error },
  onTotpCode(e) { this.pw.code = e.target.value.replace(/\D/g, '').slice(0, 6); e.target.value = this.pw.code },
  copySecret() { this.copy(this.pw.secret, 'Secret') },
  async savePassword() {
    const f = this.pw
    f.error = ''
    f.field = ''
    const fail = (field, msg) => { f.field = field; f.error = msg; this.$nextTick(() => { const el = this.$refs.pwDlg.querySelector('[aria-invalid="true"]'); if (el) el.focus() }) }
    if (!f.current) return fail('current', 'Enter your current password')
    if (f.password && f.password.length < 10) return fail('password', 'Use at least 10 characters')
    if (f.password !== f.repeat) return fail('repeat', 'The passwords do not match')
    if (!f.password && f.totp === 'keep') return fail('password', 'Enter a new password or change two-factor')
    if (f.totp === 'new' && !/^\d{6}$/.test(f.code)) return fail('code', 'Enter the 6-digit code from the app')
    f.busy = true
    try {
      await this.api('POST', '/api/system/password', { current: f.current, password: f.password, totp: f.totp, secret: f.secret, code: f.code })
      this.closeDialog('pwDlg')
      this.toast('ok', 'Saved. Other sessions were signed out.')
      await this.load_system()
    } catch (e) { fail(e.data.field || '', e.message) } finally { f.busy = false }
  },
  get backups() { return this.sys.backups || [] },
  get noBackups() { return this.backups.length === 0 },
  bkWhen(b) { return fmtAbs(Date.parse(b.at)) },
  bkHref(b) { return '/api/system/backups/' + enc(b.name) },
  bkRestoreLabel(b) { return 'Restore ' + b.name },
  bkDownloadLabel(b) { return 'Download ' + b.name },
  async backupNow() {
    try { const r = await this.api('POST', '/api/system/backup'); this.toast('ok', 'Backup ' + r.name + ' created'); await this.load_system() } catch (e) { this.toast('fail', e.message) }
  },
  askRestore(b) {
    this.ask({
      title: 'Restore ' + b.name + '?', text: 'Instances, peers, egress and settings are replaced with this backup, then the server restarts and you sign in again.',
      typed: 'restore', label: 'Restore and restart', danger: true,
      async run() { await this.api('POST', '/api/system/restore', { file: b.name, confirm: 'restore' }); this.waitRestart('Restoring ' + b.name, '-u awgx-restore -u awgx-serve') }
    })
  },

  suIs(p) { return this.su.phase === p },
  async startSetup() {
    this.su = { phase: 'inspect', data: null, pick: [], fixes: [], legacy: [], bkOld: false, endpoint: '', ipv6: false, clients: '', egress: '', conf: '', token: '', ks: false, idx: 0, summary: [], result: null, error: '' }
    try {
      const d = await this.api('GET', '/api/setup?fresh=1')
      this.su.data = d
      this.su.pick = this.suSuggested
      this.su.fixes = d.fixes.map(f => f.id)
      this.su.legacy = []
      this.su.endpoint = d.endpoint_wrong ? d.suggested_endpoint : d.endpoint
      this.su.ipv6 = d.ipv6 && d.has_ipv6
      this.su.egress = d.default_egress
      this.su.ks = d.killswitch
      this.su.phase = 'pick'
    } catch (e) { this.su.phase = 'intro'; this.toast('fail', e.message) }
  },
  get suD() { return this.su.data || { fixes: [], legacy: [], checks: {}, egress_choices: [] } },
  get suSuggested() {
    const d = this.suD, s = []
    if (d.fixes.length || d.legacy.length || d.checks.fail) s.push('check')
    if (d.endpoint_wrong) s.push('endpoint')
    if (d.ipv6 && !d.has_ipv6) s.push('ipv6')
    if (!d.clients) s.push('clients')
    if (d.egress_draft !== d.egress_live) s.push('egress')
    return s
  },
  suState(p) {
    const d = this.suD
    if (p.id === 'check') {
      const parts = []
      if (d.checks.fail) parts.push(plural(d.checks.fail, 'failure', 'failures'))
      if (d.checks.warn) parts.push(plural(d.checks.warn, 'warning', 'warnings'))
      if (d.fixes.length) parts.push(plural(d.fixes.length, 'fix', 'fixes'))
      if (d.legacy.length) parts.push(plural(d.legacy.length, 'old setup', 'old setups'))
      return parts.length ? parts.join(', ') : 'All ' + d.checks.total + ' checks pass'
    }
    if (p.id === 'endpoint') return d.endpoint_wrong ? 'Clients use ' + d.endpoint + ', public address ' + d.public_ip : 'Clients use ' + d.endpoint
    if (p.id === 'ipv6') return (d.ipv6 ? 'On' : 'Off') + (d.has_ipv6 ? '' : ', the server has no IPv6 route')
    if (p.id === 'clients') return plural(d.clients, 'client exists', 'clients exist')
    const label = { direct: 'straight to the internet', uplink: 'cascade through an uplink', xui: 'through xui' }
    if (d.egress_draft !== d.egress_live) return 'draft ' + (label[d.egress_draft] || d.egress_draft) + ' is not applied'
    return label[d.egress_live] || d.egress_live
  },
  suPicked(p) { return this.su.pick.includes(p.id) ? 'true' : 'false' },
  suSuggest(p) { return this.suSuggested.includes(p.id) },
  suFlip(p) { const i = this.su.pick.indexOf(p.id); if (i < 0) this.su.pick.push(p.id); else this.su.pick.splice(i, 1) },
  get suNoPick() { return this.su.pick.length === 0 },
  get suFlow() { return SETUP_PICKS.map(p => p.id).filter(id => this.su.pick.includes(id)).concat('review') },
  get suStep() { return this.suFlow[this.su.idx] },
  suStepIs(s) { return this.su.phase === 'step' && this.suStep === s },
  get suStepper() {
    const items = [{ id: 'pick', n: 1, t: 'What to change' }].concat(this.suFlow.map((s, i) => ({ id: s, n: i + 2, t: SETUP_TITLES[s] })))
    const cur = this.su.phase === 'pick' ? 0 : this.su.phase === 'step' ? this.su.idx + 1 : items.length
    return items.map((x, i) => Object.assign(x, { state: i < cur ? 'done' : i === cur ? 'current' : 'todo' }))
  },
  get suStepNotReview() { return this.su.phase === 'step' && this.suStep !== 'review' },
  get suErrText() { return this.su.result ? this.su.result.error || '' : '' },
  get suShowStepper() { return this.su.phase === 'pick' || this.su.phase === 'step' },
  suBegin() { if (!this.su.pick.length) return; this.su.idx = 0; this.su.phase = 'step'; this.suEnter() },
  suBack() { if (this.su.idx === 0) this.su.phase = 'pick'; else { this.su.idx--; this.suEnter() } },
  async suNext() {
    this.su.error = ''
    if (this.suStep === 'endpoint' && !this.su.endpoint.trim()) { this.su.error = 'Enter an address or host name'; return }
    if (this.suStep === 'clients' && !this.suClientList.length) { this.su.error = 'Enter at least one name'; return }
    if (this.suStep === 'clients' && this.suClientList.some(n => !PEER_RE.test(n))) { this.su.error = 'Names use latin letters and digits, - only, up to 32 characters'; return }
    if (this.suStep === 'egress' && this.su.egress === 'uplink' && !/\[Peer\]/.test(this.su.conf)) { this.su.error = 'Paste the client .conf of the other server'; return }
    if (this.suStep === 'egress' && this.su.egress === 'xui' && !this.su.token.trim()) { this.su.error = 'Paste the 3x-ui API token'; return }
    this.su.idx++
    this.suEnter()
  },
  async suEnter() {
    if (this.suStep !== 'review') return
    this.su.summary = null
    try { const r = await this.api('POST', '/api/setup/summary', this.suBody); this.su.summary = r.summary || [] } catch (e) { this.su.error = e.message }
  },
  get suClientList() { return this.su.clients.split(',').map(s => s.trim()).filter(Boolean) },
  get suBody() {
    const s = this.su
    return { pick: this.suFlow.filter(x => x !== 'review'), fixes: s.fixes, legacy: s.legacy, backup_old: s.bkOld, endpoint: s.endpoint.trim(), ipv6: s.ipv6, clients: this.suClientList, egress: s.egress, uplink_conf: s.conf, xui_token: s.token.trim(), killswitch: s.ks }
  },
  suFixChecked(f) { return this.su.fixes.includes(f.id) ? 'true' : 'false' },
  suFixFlip(f) { const i = this.su.fixes.indexOf(f.id); if (i < 0) this.su.fixes.push(f.id); else this.su.fixes.splice(i, 1) },
  get suLegacy() { return this.suD.legacy.map(x => Object.assign({ i: x.kind + '/' + x.name }, x)) },
  suLegChecked(x) { return this.su.legacy.includes(x.i) ? 'true' : 'false' },
  suLegFlip(x) { const i = this.su.legacy.indexOf(x.i); if (i < 0) this.su.legacy.push(x.i); else this.su.legacy.splice(i, 1) },
  get suHasFixes() { return this.suD.fixes.length > 0 },
  get suHasLegacy() { return this.suD.legacy.length > 0 },
  get suCheckEmpty() { return !this.suD.fixes.length && !this.suD.legacy.length },
  get suNat() { return !!this.suD.behind_nat },
  get suNatText() { return 'This server sits behind NAT: its own address is ' + this.suD.local_ip + ', the internet sees ' + this.suD.public_ip + '. Clients need the public address.' },
  get suEndpointChanged() { return this.su.endpoint.trim() && this.su.endpoint.trim() !== this.suD.endpoint },
  get suEndpointImpact() { return 'Changing it re-issues configs for ' + plural(this.suD.clients, 'client', 'clients') + ' of ' + this.suD.instance + '.' },
  suIPv6Is(on) { return this.su.ipv6 === on ? 'true' : 'false' },
  suSetIPv6(on) { this.su.ipv6 = on },
  get suNoIPv6Route() { return !this.suD.has_ipv6 },
  get suIPv6Changed() { return this.su.ipv6 !== !!this.suD.ipv6 },
  get suIPv6Impact() { return 'Every client of ' + this.suD.instance + ' (' + plural(this.suD.clients, 'client', 'clients') + ') must import its config again.' },
  useSuggested() { this.su.endpoint = this.suD.suggested_endpoint },
  get suPublicErr() { return this.suD.public_err ? 'The public address lookup failed (' + this.suD.public_err + '), so a wrong endpoint was not detected.' : '' },
  suEgChecked(c) { return this.su.egress === c.value ? 'true' : 'false' },
  suEgPick(c) { this.su.egress = c.value },
  get suUplink() { return this.su.egress === 'uplink' },
  get suXui() { return this.su.egress === 'xui' },
  get suKsShown() { return this.su.egress === 'uplink' || this.su.egress === 'xui' },
  get suBkOld() { return this.su.bkOld ? 'true' : 'false' },
  suFlipBkOld() { this.su.bkOld = !this.su.bkOld },
  get suKs() { return this.su.ks ? 'true' : 'false' },
  suFlipKs() { this.su.ks = !this.su.ks },
  get suSummaryLoading() { return this.su.summary === null && !this.su.error },
  get suSummaryList() { return this.su.summary || [] },
  get suHasSummary() { return this.suSummaryList.length > 0 },
  get suNothing() { return Array.isArray(this.su.summary) && this.su.summary.length === 0 },
  get suRunOff() { return this.jobRunning || !this.suHasSummary },
  async suRun() {
    try {
      this.su.phase = 'run'
      await this.startJob(this.api('POST', '/api/setup', this.suBody), function (j) {
        this.su.result = Object.assign({ clients: [], notes: [] }, j.result, j.error ? { error: j.error } : {})
        this.su.phase = 'result'
      })
    } catch (e) { this.su.phase = 'step'; this.su.error = e.message }
  },
  get suRunText() { return this.job && this.jobRunning ? this.jobStep : 'Starting…' },
  get suRunPct() { return this.jobPct },
  get suOk() { return !!this.su.result && !this.su.result.error },
  get suFail() { return !!this.su.result && !!this.su.result.error },
  get suResClients() { return (this.su.result && this.su.result.clients) || [] },
  get suHasClients() { return this.suResClients.length > 0 },
  get suNotes() { return (this.su.result && this.su.result.notes) || [] },
  suExport(n) { this.openExport({ name: n }) },
  suRestart() { this.su.phase = 'intro'; this.su.result = null }
}))
