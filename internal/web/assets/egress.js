const EG_TABS = [
  { id: 'routes', t: 'Routes' }, { id: 'profiles', t: 'Profiles' }, { id: 'uplinks', t: 'Uplinks' }, { id: 'xui', t: '3x-ui' }, { id: 'history', t: 'History' }
]
const FLOW = [{ n: 1, t: 'Draft' }, { n: 2, t: 'Plan' }, { n: 3, t: 'Apply' }]

AWGX.push(() => ({
  egress: { default: {}, peers: {}, profiles: [], uplinks: [], plan: { diff: [], warnings: [], issues: [] }, status: { applied: 0, history: [] } },
  egressLoaded: false,
  xui: { installed: false, status: null, instance: '', saved_auth: false },
  xuiAt: 0,
  xuiBusy: false,
  xuiAgain: false,
  flow: FLOW,
  applyBusy: false,
  applyChecks: [],
  applyWarn: '',
  applyBase: 0,
  histOpen: 0,
  ulTests: {},
  pf: { edit: false, name: '', kind: 'iface', hops: [], hop: '', socks: '', user: '', password: '', fallback: 'drop', busy: false, error: '', field: '' },
  ul: { edit: '', name: '', conf: '', mtu: '', routed: false, busy: false, error: '', field: '', warnings: [] },
  asg: { instance: '', peers: [], profile: 'direct', ks: false, busy: false, error: '' },
  xc: { instance: '', token: '', fallback: false, ks: true, busy: false, error: '' },
  xd: { keep: false, busy: false, error: '' },

  async load_egress() {
    if (Date.now() - this.xuiAt > 60000) this.loadXui()
    const [e] = await Promise.all([this.api('GET', '/api/egress'), this.peersLoaded ? null : this.load_peers(false)])
    this.egress = e
    this.egressLoaded = true
  },
  loadXui() {
    if (this.xuiBusy) { this.xuiAgain = true; return }
    this.xuiBusy = true
    this.api('GET', '/api/xui').then(x => { this.xui = x; this.xuiAt = Date.now() }).catch(() => {}).finally(() => {
      this.xuiBusy = false
      if (this.xuiAgain) { this.xuiAgain = false; this.loadXui() }
    })
  },
  get egTabs() { return EG_TABS.filter(t => t.id !== 'xui' || this.xui.installed) },
  get egTab() { return this.egTabs.some(t => t.id === this.sub) ? this.sub : 'routes' },
  egTabIs(t) { return this.egTab === t },
  egTabCur(t) { return this.egTab === t.id ? 'page' : false },
  egTabHref(t) { return '#/egress/' + t.id },
  egTabCount(t) {
    if (t.id === 'profiles') return String(this.egress.profiles.length)
    if (t.id === 'uplinks') return String((this.egress.uplinks || []).length)
    if (t.id === 'routes') return String(Object.keys(this.egress.peers || {}).length + Object.keys(this.egress.default || {}).length)
    return ''
  },
  hasTabCount(t) { return !!this.egTabCount(t) },

  get planDiff() { return diffLines(this.egress.plan.diff) },
  get hasPlan() { return this.planDiff.length > 0 },
  get planWarnings() { return this.egress.plan.warnings || [] },
  get planIssues() { return this.egress.plan.issues || [] },
  get hasChanges() { return !!this.egress.plan.changed },
  get changesText() { return this.draftCount ? plural(this.draftCount, 'change', 'changes') : 'profile settings changed' },
  get draftCount() { return (this.egress.plan.diff || []).filter(l => l[0] === '+' || l[0] === '-').length },
  get draftAdds() { return '+' + (this.egress.plan.diff || []).filter(l => l[0] === '+').length },
  get draftDels() { return '−' + (this.egress.plan.diff || []).filter(l => l[0] === '-').length },
  get noChanges() { return this.egressLoaded && !this.hasChanges && !this.hasDrift },
  get drift() { return (this.egress.status && this.egress.status.drift) || [] },
  get hasDrift() { return this.drift.length > 0 },
  get driftLines() { return diffLines(this.drift) },
  get showDrift() { return this.hasDrift && !this.hasChanges },
  get hasWarnings() { return this.planWarnings.length > 0 },
  get hasIssues() { return this.planIssues.length > 0 },
  get draftBar() { return this.egressLoaded && this.hasChanges },
  get draftText() { return 'Draft differs from snapshot #' + this.egress.status.applied + ': ' + this.changesText },
  get draftNote() {
    if (this.hasIssues) return plural(this.planIssues.length, 'error blocks', 'errors block') + ' the apply'
    if (this.hasWarnings) return plural(this.planWarnings.length, 'warning', 'warnings')
    return 'Not applied yet'
  },
  get draftNoteClass() { return 'small ' + (this.hasIssues ? 'fail-text' : this.hasWarnings ? 'warn-text' : 'muted') },
  get egStep() { return this.showResult ? 3 : this.hasChanges ? 2 : 1 },
  flowState(f) { return f.n < this.egStep ? 'done' : f.n === this.egStep ? 'current' : 'todo' },
  get egStatusText() {
    if (this.hasChanges) return 'Draft has ' + this.changesText
    if (this.hasDrift) return 'Live rules differ from snapshot #' + this.egress.status.applied
    return 'Draft matches applied snapshot #' + this.egress.status.applied
  },
  get applyDisabled() { return this.applyBusy || (this.hasChanges ? this.hasIssues : !this.hasDrift) },
  get applyReason() {
    if (!this.hasChanges && this.hasDrift) return 'Apply restores the live rules of the applied snapshot.'
    if (!this.hasChanges) return 'Nothing to apply.'
    if (this.hasIssues) return 'Fix the errors before applying.'
    return 'Rules are applied now, then checked through a test peer.'
  },
  get showResult() { return this.applyChecks.length > 0 },
  get applyResult() { return diffLines(this.applyChecks) },
  get applyIdle() { return !this.applyBusy },
  get planIdle() { return !this.showResult },
  openPlan() { this.openDialog('planDlg') },
  get hasApplyWarn() { return !!this.applyWarn },
  get undoLabel() { return 'Roll back to #' + this.applyBase },
  get canUndo() { return this.applyBase > 0 && !this.applyBusy },
  async runApply(path) {
    this.applyBusy = true
    this.applyChecks = []
    this.applyWarn = ''
    try {
      const r = await this.api('POST', path)
      this.applyChecks = r.checks || []
      this.applyWarn = r.warning || ''
      this.applyBase = r.base || 0
      this.xuiAt = 0
      if (r.converged) this.toast('ok', 'Live rules match the applied snapshot again')
      else if (this.applyWarn) this.toast('warn', 'Snapshot #' + r.snapshot + ' is applied, but a post-check failed')
      else this.toast('ok', 'Snapshot #' + r.snapshot + ' applied')
      if (!this.applyWarn) { this.applyChecks = []; this.closeDialog('planDlg') }
      await this.refresh(true)
    } catch (e) {
      this.toast('fail', e.message)
      if (e.data.checks && e.data.checks.length) this.applyChecks = e.data.checks
    } finally { this.applyBusy = false }
  },
  applyEgress() { if (!this.applyDisabled) return this.runApply('/api/egress/apply') },
  undoApply() { if (this.canUndo) return this.runApply('/api/egress/history/' + this.applyBase + '/rollback') },

  get egDefaults() {
    return Object.keys(this.egress.default).map(k => ({ instance: k, profile: this.egress.default[k].profile, killswitch: !!this.egress.default[k].killswitch }))
  },
  get routeOptions() { return ['direct'].concat(this.egress.profiles.map(p => p.name)) },
  ksDisabled(r) { return r.profile === 'direct' },
  ksChecked(r) { return r.killswitch && r.profile !== 'direct' ? 'true' : 'false' },
  ksLabel(r) { return 'Killswitch for ' + (r.instance || r.peer) },
  routeLabel(r) { return 'Default route for ' + r.instance },
  routeSelected(r, o) { return r.profile === o },
  async setDefault(r, e) {
    try {
      const profile = e.target.value
      await this.api('PUT', '/api/egress/assign', { instance: r.instance, profile, killswitch: profile !== 'direct' && r.killswitch })
      await this.load_egress()
    } catch (err) {
      e.target.value = r.profile
      this.toast('fail', err.message)
    }
  },
  async flipDefaultKs(r) {
    if (r.profile === 'direct') return
    try {
      await this.api('PUT', '/api/egress/assign', { instance: r.instance, profile: r.profile, killswitch: !r.killswitch })
      await this.load_egress()
    } catch (e) { this.toast('fail', e.message) }
  },
  get egOverrides() {
    return Object.keys(this.egress.peers).map(k => {
      const p = this.peers.find(x => x.name === k) || {}
      return { peer: k, instance: p.instance || '', profile: this.egress.peers[k].profile, killswitch: !!this.egress.peers[k].killswitch }
    })
  },
  get noOverrides() { return this.egOverrides.length === 0 },
  editOverride(r) { this.openAssign([r.peer], r.profile, r.killswitch, r.instance) },
  async removeOverride(r) {
    try {
      await this.api('PUT', '/api/peers/' + enc(r.peer) + '/egress', { profile: 'inherit', killswitch: false })
      this.toast('ok', r.peer + ' now uses the instance default (in the draft)')
      await this.load_egress()
    } catch (e) { this.toast('fail', e.message) }
  },
  removeLabel(r) { return 'Use the instance default for ' + r.peer },

  openAssign(peers, profile, ks, instance) {
    this.asg = { instance: instance || this.instanceNames[0] || '', peers: Array.isArray(peers) ? peers.slice() : [], profile: typeof profile === 'string' ? profile : this.routeOptions[1] || 'direct', ks: ks === true, busy: false, error: '' }
    this.openDialog('asgDlg')
  },
  get asgPeers() { return this.peers.filter(p => p.instance === this.asg.instance) },
  asgChecked(p) { return this.asg.peers.includes(p.name) ? 'true' : 'false' },
  asgFlip(p) { const i = this.asg.peers.indexOf(p.name); if (i < 0) this.asg.peers.push(p.name); else this.asg.peers.splice(i, 1) },
  asgRoute(p) { const a = this.egress.peers[p.name]; return a ? a.profile + (a.killswitch ? ' · killswitch' : '') : 'default' },
  get asgAll() { return this.asgPeers.length > 0 && this.asgPeers.every(p => this.asg.peers.includes(p.name)) ? 'true' : 'false' },
  asgFlipAll() { this.asg.peers = this.asgAll === 'true' ? [] : this.asgPeers.map(p => p.name) },
  asgInst() { this.asg.peers = [] },
  get asgKsDisabled() { return this.asg.profile === 'direct' },
  get asgKs() { return this.asg.ks && this.asg.profile !== 'direct' ? 'true' : 'false' },
  asgFlipKs() { if (this.asg.profile !== 'direct') this.asg.ks = !this.asg.ks },
  get asgCount() { return this.asg.peers.length ? 'Assign ' + plural(this.asg.peers.length, 'peer', 'peers') : 'Select peers' },
  async submitAssign() {
    if (!this.asg.peers.length) { this.asg.error = 'Select at least one peer'; return }
    this.asg.busy = true
    this.asg.error = ''
    try {
      await this.api('PUT', '/api/egress/assign', { instance: this.asg.instance, peers: this.asg.peers, profile: this.asg.profile, killswitch: this.asg.ks && this.asg.profile !== 'direct' })
      this.closeDialog('asgDlg')
      this.toast('ok', 'Routes changed in the draft')
      await this.load_egress()
    } catch (e) { this.asg.error = e.message } finally { this.asg.busy = false }
  },

  healthClass(p) { return 'badge ' + ({ ok: 'badge-ok', degraded: 'badge-warn', down: 'badge-fail' }[p.health] || 'badge-muted') },
  fallbackText(p) { return p.fallback || 'drop' },
  profUsed(p) {
    const n = Object.values(this.egress.peers).filter(a => a.profile === p.name).length + Object.values(this.egress.default).filter(a => a.profile === p.name).length
    return n ? plural(n, 'route', 'routes') : 'unused'
  },
  profManaged(p) { return p.kind === 'xui' },
  profDeletable(p) { return p.kind !== 'xui' },
  openProfile(p) {
    const e = !!p && !!p.name
    this.pf = { edit: e, name: e ? p.name : '', kind: e ? p.kind : 'iface', hops: e ? (p.hops || []).slice() : [], hop: '', socks: e ? p.socks || '' : '', user: e ? p.user || '' : '', password: '', udp: e ? !p.no_udp : true, dns: e ? !!p.dns_hijack : null, fallback: e ? p.fallback || 'drop' : 'drop', busy: false, error: '', field: '' }
    this.openDialog('profDlg')
  },
  newProfile() { this.openProfile(null) },
  get pfTyped() { return this.pf.kind !== 'xui' },
  get pfTitle() { return this.pf.edit ? 'Edit profile ' + this.pf.name : 'Add egress profile' },
  pfKind(k) { return this.pf.kind === k ? 'true' : 'false' },
  setPfKind(k) { if (k !== this.pf.kind) this.pf.dns = null; this.pf.kind = k },
  get pfIface() { return this.pf.kind === 'iface' },
  get pfSocks() { return this.pf.kind === 'socks5' },
  get pfHops() { return this.pf.hops.map((h, i) => ({ h, i, n: i + 1, label: i === 0 ? 'primary' : 'failover ' + i })) },
  get pfUdp() { return this.pf.udp ? 'true' : 'false' },
  flipPfUdp() { this.pf.udp = !this.pf.udp },
  pfDnsOn() { return this.pf.dns === null ? this.pf.kind === 'socks5' : this.pf.dns },
  get pfDns() { return this.pfDnsOn() ? 'true' : 'false' },
  flipPfDns() { this.pf.dns = !this.pfDnsOn() },
  get pfNoHops() { return this.pf.hops.length === 0 },
  get pfSuggest() {
    const ups = (this.egress.uplinks || []).map(u => ({ name: u.name, d: 'uplink' }))
    const ins = this.overview.instances.map(i => ({ name: i.name, d: 'instance' }))
    return ups.concat(ins).filter(x => !this.pf.hops.includes(x.name))
  },
  addHop(s) { if (s && !this.pf.hops.includes(s.name)) this.pf.hops.push(s.name) },
  addHopTyped() { const v = this.pf.hop.trim(); if (v && !this.pf.hops.includes(v)) this.pf.hops.push(v); this.pf.hop = '' },
  hopKey(e) { if (e.key === 'Enter') { e.preventDefault(); this.addHopTyped() } },
  hopUp(x) { if (x.i > 0) { const h = this.pf.hops; h.splice(x.i - 1, 0, h.splice(x.i, 1)[0]) } },
  hopDown(x) { const h = this.pf.hops; if (x.i < h.length - 1) h.splice(x.i + 1, 0, h.splice(x.i, 1)[0]) },
  hopRemove(x) { this.pf.hops.splice(x.i, 1) },
  hopFirst(x) { return x.i === 0 },
  hopLast(x) { return x.i === this.pf.hops.length - 1 },
  hopUpLabel(x) { return 'Move ' + x.h + ' up' },
  hopDownLabel(x) { return 'Move ' + x.h + ' down' },
  hopRmLabel(x) { return 'Remove ' + x.h },
  get pfFallbacks() {
    return [{ v: 'direct', t: 'Go direct' }, { v: 'drop', t: 'Drop traffic' }].concat(this.egress.profiles.filter(p => p.name !== this.pf.name).map(p => ({ v: p.name, t: 'Profile ' + p.name })))
  },
  get pfPwHint() { return this.pf.edit ? 'Leave empty to keep the current password. Clear the user to turn authentication off.' : 'Optional.' },
  pfErr(f) { return this.pf.field === f ? this.pf.error : '' },
  get pfNew() { return !this.pf.edit },
  pfInvalid(f) { return this.pf.field === f ? 'true' : 'false' },
  get pfGeneralError() { return ['name', 'hops', 'socks'].includes(this.pf.field) ? '' : this.pf.error },
  async saveProfile() {
    const f = this.pf
    f.error = ''
    f.field = ''
    if (!f.edit && (!PROFILE_RE.test(f.name) || RESERVED_PROFILES.includes(f.name))) { f.field = 'name'; f.error = !f.name ? 'Name is required' : RESERVED_PROFILES.includes(f.name) ? 'The name ' + f.name + ' is reserved' : 'Use up to 31 lowercase letters, digits or -, starting with a letter'; return }
    if (f.kind === 'iface' && !f.hops.length) { f.field = 'hops'; f.error = 'Add at least one interface'; return }
    if (f.kind === 'socks5' && !/^(\[[0-9a-fA-F:.]+\]|[\w.-]+):\d+$/.test(f.socks.trim())) { f.field = 'socks'; f.error = 'Use IP:PORT, like 127.0.0.1:1080 or [::1]:1080'; return }
    f.busy = true
    const body = { name: f.name, kind: f.kind, hops: f.kind === 'iface' ? f.hops : [], socks: f.socks.trim(), user: f.user.trim(), password: f.password, no_udp: !f.udp, dns_hijack: this.pfDnsOn(), fallback: f.fallback }
    try {
      if (f.edit) await this.api('PUT', '/api/egress/profiles/' + enc(f.name), body)
      else await this.api('POST', '/api/egress/profiles', body)
      this.closeDialog('profDlg')
      this.toast('ok', 'Profile ' + f.name + ' saved in the draft')
      await this.load_egress()
    } catch (e) { f.error = e.message; f.field = e.data.field || '' } finally { f.busy = false }
  },
  askDeleteProfile(p) {
    this.ask({
      title: 'Delete profile ' + p.name + '?', text: 'Only an unused profile can be deleted: reassign its routes and fallbacks first. The change goes to the draft.', label: 'Delete profile', danger: true,
      async run() { await this.api('DELETE', '/api/egress/profiles/' + enc(p.name)); this.toast('ok', 'Profile deleted in the draft'); await this.load_egress() }
    })
  },
  profEditLabel(p) { return 'Edit ' + p.name },
  profDelLabel(p) { return 'Delete ' + p.name },

  get uplinks() { return this.egress.uplinks || [] },
  get noUplinks() { return this.egressLoaded && this.uplinks.length === 0 },
  ulUsed(u) { return u.used_by && u.used_by.length ? 'used by ' + u.used_by.join(', ') : 'not used by a profile' },
  ulMode(u) { return u.routed ? 'routed, no NAT' : 'NAT' },
  ulTransfer(u) { return '↓ ' + fmtBytes(u.rx_bytes) + '  ↑ ' + fmtBytes(u.tx_bytes) },
  ulHasTest(u) { return !!this.ulTests[u.name] },
  ulTestLines(u) { const t = this.ulTests[u.name]; return t ? diffLines(t.checks.concat(t.error ? ['fail ' + t.error] : [])) : [] },
  ulIdle(u) { return !this.ulTesting(u) },
  ulTesting(u) { return !!(this.ulTests[u.name] && this.ulTests[u.name].testing) },
  async testUplink(u) {
    if (this.ulTesting(u)) return
    this.ulTests = Object.assign({}, this.ulTests, { [u.name]: { checks: ['… testing'], error: '', testing: true } })
    try {
      const r = await this.api('POST', '/api/uplinks/' + enc(u.name) + '/test')
      this.ulTests = Object.assign({}, this.ulTests, { [u.name]: r })
    } catch (e) { this.ulTests = Object.assign({}, this.ulTests, { [u.name]: { checks: [], error: e.message } }) }
  },
  askRemoveUplink(u) {
    const used = u.used_by && u.used_by.length
    this.ask({
      title: 'Remove uplink ' + u.name + '?', text: used ? 'Profile ' + u.used_by.join(', ') + ' uses it and will fail over to its next interface or fallback.' : 'The client interface to ' + u.endpoint + ' is removed.',
      label: 'Remove uplink', danger: true,
      async run() { await this.api('DELETE', '/api/uplinks/' + enc(u.name)); this.toast('ok', 'Uplink ' + u.name + ' removed'); await this.load_egress() }
    })
  },
  openUplink() { this.ul = { edit: '', name: '', conf: '', mtu: '', port: '', routed: false, busy: false, error: '', field: '', warnings: [] }; this.openDialog('ulDlg') },
  async editUplink(u) {
    this.ul = { edit: u.name, name: u.name, conf: '', orig: '', mtu: '', cur: u.mtu || 0, port: u.port ? String(u.port) : '', wasPort: u.port ? String(u.port) : '', curPort: u.listen_port || 0, routed: !!u.routed, was: !!u.routed, busy: false, error: '', field: '', warnings: [] }
    this.openDialog('ulDlg')
    try { const r = await this.api('GET', '/api/uplinks/' + enc(u.name)); this.ul.conf = r.conf; this.ul.orig = r.conf } catch (e) { this.ul.error = e.message }
  },
  get ulConfChanged() { return this.ul.conf.trim() !== (this.ul.orig || '').trim() },
  get ulEditing() { return !!this.ul.edit },
  get ulTitle() { return this.ul.edit ? 'Edit uplink ' + this.ul.edit : 'Add uplink' },
  get ulSubmit() { return this.ul.edit ? 'Save and restart' : 'Add uplink' },
  get ulConfLabel() { return this.ul.edit ? 'Client .conf' : 'Client .conf from the other server' },
  get ulConfHint() { return this.ul.edit ? 'The current config. Paste a new one from the other server to replace it; its MTU is fitted to this server unless you set one below.' : 'Create a client on the other server and paste its whole config.' },
  get ulDoneText() { return this.ul.edit ? 'saved and restarted.' : 'added and up.' },
  get ulMTULabel() { return this.ulConfChanged ? 'MTU (empty: fit the new config to this server)' : 'MTU (empty: keep ' + this.ul.cur + ')' },
  get ulMTUHolder() { return this.ul.cur ? String(this.ul.cur) : '' },
  get ulPortChanged() { return String(this.ul.port).trim() !== (this.ul.wasPort || '') },
  get ulPortHolder() { return this.ul.curPort ? 'random, now ' + this.ul.curPort : 'random' },
  get xuiInst() { return this.xui.instance || 'the instance' },
  get xcInst() { return this.xc.instance || this.xuiInst },
  ulMTU(u) { return u.mtu ? String(u.mtu) : '—' },
  ulRate(u) { return rateText(u.rate) },
  ulPort(u) { return u.port ? String(u.port) : u.listen_port ? 'random (' + u.listen_port + ')' : 'random' },
  async speedUplink(u) {
    try { await this.startJob(this.api('POST', '/api/uplinks/' + enc(u.name) + '/speed')) } catch (e) { this.toast('fail', e.message) }
  },
  get ulRouted() { return this.ul.routed ? 'true' : 'false' },
  flipUlRouted() { this.ul.routed = !this.ul.routed },
  ulErr(f) { return this.ul.field === f ? this.ul.error : '' },
  ulInvalid(f) { return this.ul.field === f ? 'true' : 'false' },
  get ulGeneralError() { return this.ul.field ? '' : this.ul.error },
  get ulDone() { return this.ul.warnings.length > 0 },
  get ulForm() { return this.ul.warnings.length === 0 },
  async saveUplink() {
    const f = this.ul
    f.error = ''
    f.field = ''
    if (!f.edit && !/^awg\d{1,4}$/.test(f.name)) { f.field = 'name'; f.error = f.name ? 'Use awg and a number, like awg1' : 'Name is required'; return }
    if ((!f.edit || this.ulConfChanged) && (!/\[Interface\]/.test(f.conf) || !/\[Peer\]/.test(f.conf))) { f.field = 'conf'; f.error = 'Paste a full client .conf with [Interface] and [Peer]'; return }
    let mtu = 0
    if (f.edit && String(f.mtu).trim()) {
      mtu = Number(f.mtu)
      if (!/^\d+$/.test(String(f.mtu).trim())) { f.field = 'mtu'; f.error = 'Enter a number'; return }
    }
    const port = String(f.port).trim()
    if (port && (!/^\d+$/.test(port) || Number(port) < 1 || Number(port) > 65535)) { f.field = 'port'; f.error = 'Enter a port from 1 to 65535, or leave it empty'; return }
    if (f.edit && !this.ulConfChanged && (!mtu || mtu === f.cur) && f.routed === f.was && !this.ulPortChanged) { this.closeDialog('ulDlg'); return }
    f.busy = true
    try {
      const r = f.edit
        ? await this.api('PUT', '/api/uplinks/' + enc(f.edit), { conf: this.ulConfChanged ? f.conf : '', mtu, port: this.ulPortChanged ? Number(port) : undefined, routed: f.routed === f.was ? undefined : f.routed })
        : await this.api('POST', '/api/uplinks', { name: f.name, conf: f.conf, port: Number(port), routed: f.routed })
      await this.load_egress()
      if (r.warnings && r.warnings.length) f.warnings = r.warnings
      else { this.closeDialog('ulDlg'); this.toast('ok', 'Uplink ' + f.name + (f.edit ? ' saved' : ' added')) }
    } catch (e) { f.error = e.message; f.field = e.data.field || '' } finally { f.busy = false }
  },

  get xs() { return this.xui.status || {} },
  get xuiConnected() { return !!this.xs.connected },
  get xuiDisconnected() { return this.xui.installed && !this.xs.connected },
  get xuiRows() {
    const s = this.xs
    const yes = v => v ? 'yes' : 'no'
    return [
      { k: 'Panel', v: s.panel + ' · v' + s.version, c: 'mono', bad: false },
      { k: 'Xray running', v: yes(s.running), bad: !s.running },
      { k: 'Inbound', v: '#' + s.inbound_id + (s.present ? '' : ' (missing)'), bad: !s.present },
      { k: 'Inbound enabled', v: yes(s.enabled), bad: !s.enabled },
      { k: 'Settings match', v: yes(s.matches), bad: !s.matches },
      { k: 'Traffic', v: '↓ ' + fmtBytes(s.down) + '  ↑ ' + fmtBytes(s.up), bad: false },
      { k: 'Routes applied', v: yes(s.applied), bad: !s.applied }
    ]
  },
  xuiVal(r) { return r.bad ? 'fail-text' : '' },
  get xuiProblem() { return this.xs.problem || this.xui.error || '' },
  get xuiRoutes() {
    const out = []
    for (const [inst, a] of Object.entries(this.egress.default)) if (a.profile === 'xui') out.push('default of ' + inst)
    for (const [peer, a] of Object.entries(this.egress.peers)) if (a.profile === 'xui') out.push('peer ' + peer)
    return out
  },
  get xuiBadge() { return 'badge ' + (this.xs.removing ? 'badge-warn' : 'badge-ok') },
  get xuiBadgeText() { return this.xs.removing ? 'disconnecting' : 'connected' },
  get xuiRemovingText() {
    const then = this.xs.keeping ? ' stays in 3x-ui, awgx forgets it' : ' is deleted from 3x-ui'
    return this.xs.applied ? 'Routes still use xui: apply the change to finish disconnecting.' : 'Finishing the disconnect: inbound #' + this.xs.inbound_id + then + '. awgx retries while 3x-ui is unreachable.'
  },
  get xdDelete() { return this.xd.keep ? 'false' : 'true' },
  flipXdDelete() { this.xd.keep = !this.xd.keep },
  get xdBtn() { return this.xd.keep ? 'btn btn-primary' : 'btn btn-destructive' },
  get xdHint() {
    return this.xd.keep ? 'Inbound #' + this.xs.inbound_id + ' (awgx-egress) stays in the panel; awgx forgets it.' : 'Inbound #' + this.xs.inbound_id + ' (awgx-egress) is deleted with its traffic stats. Other inbounds are not touched.'
  },
  openXuiDisc() { this.xd = { keep: false, busy: false, error: '' }; this.openDialog('xdDlg') },
  async disconnectXui() {
    this.xd.busy = true
    this.xd.error = ''
    try {
      await this.startJob(this.api('POST', '/api/xui/disconnect', { keep_inbound: this.xd.keep }), function () { this.xuiAt = 0; this.load_egress() })
      this.closeDialog('xdDlg')
    } catch (e) { this.xd.error = e.message } finally { this.xd.busy = false }
  },
  openXui() { this.xc = { instance: this.xui.instance || '', token: '', fallback: false, ks: true, busy: false, error: '' }; this.openDialog('xuiDlg') },
  get xcFallback() { return this.xc.fallback ? 'true' : 'false' },
  flipXcFallback() { this.xc.fallback = !this.xc.fallback },
  get xcToken() { return !this.xc.fallback },
  get xcTokenHint() { return this.xui.saved_auth ? 'Leave empty to use the saved token or login.' : 'In the panel: Settings → Security → API token.' },
  get xcKs() { return this.xc.ks ? 'true' : 'false' },
  flipXcKs() { this.xc.ks = !this.xc.ks },
  async connectXui() {
    if (!this.xc.fallback && !this.xc.token.trim() && !this.xui.saved_auth) { this.xc.error = 'Paste the API token, or use the fallback token'; return }
    this.xc.busy = true
    this.xc.error = ''
    try {
      await this.startJob(this.api('POST', '/api/xui/connect', { instance: this.xc.instance, token: this.xc.token.trim(), fallback_token: this.xc.fallback, killswitch: this.xc.ks }), function () { this.xuiAt = 0; this.load_egress() })
      this.closeDialog('xuiDlg')
    } catch (e) { this.xc.error = e.message } finally { this.xc.busy = false }
  },

  get history() { return this.egress.status.history || [] },
  histClass(h) { return 'badge ' + ({ confirmed: 'badge-ok', failed: 'badge-fail' }[h.status] || 'badge-muted') },
  histStatus(h) { return h.status.replace('_', ' ') },
  histTitle(h) { return 'Snapshot #' + h.id },
  histWhen(h) { return fmtAbs(Date.parse(h.created_at)) },
  histExpanded(h) { return this.histOpen === h.id ? 'true' : 'false' },
  histShown(h) { return this.histOpen === h.id },
  toggleHist(h) { this.histOpen = this.histOpen === h.id ? 0 : h.id },
  histDiff(h) { return diffLines((h.diff || '').split('\n').filter(Boolean)) },
  histChecks(h) { return diffLines((h.checks || '').split('\n').filter(Boolean)) },
  histPanel(h) { return 'hist-' + h.id },
  histCurrent(h) { return h.id === this.egress.status.applied },
  histCanRoll(h) { return h.status !== 'failed' && (h.id !== this.egress.status.applied || this.hasChanges) },
  histRollLabel(h) { return h.id === this.egress.status.applied ? 'Reset draft to this' : 'Roll back to this' },
  askHistRollback(h) {
    if (h.id === this.egress.status.applied) {
      this.ask({
        title: 'Reset the draft to snapshot #' + h.id + '?',
        text: 'Your unapplied draft changes are discarded. Nothing is applied.',
        label: 'Reset draft',
        async run() {
          await this.api('POST', '/api/egress/reset')
          await this.refresh(true)
          this.toast('ok', 'Draft reset to the applied egress')
        }
      })
      return
    }
    this.ask({
      title: 'Roll back to snapshot #' + h.id + '?',
      text: 'The draft is replaced with this snapshot and applied right away.',
      label: 'Apply snapshot #' + h.id,
      async run() {
        await this.runApply('/api/egress/history/' + h.id + '/rollback')
        if (this.applyWarn) this.openPlan()
      }
    })
  }
}))
