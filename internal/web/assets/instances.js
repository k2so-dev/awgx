const OBF = [
  { id: 'junk', title: 'Junk packets', desc: 'Random packets sent before each handshake.', fields: [
    { k: 'jc', l: 'Jc', h: 'Count, 0–122' }, { k: 'jmin', l: 'Jmin', h: 'Min size, bytes' }, { k: 'jmax', l: 'Jmax', h: 'Max size, up to 1252' }] },
  { id: 'pad', title: 'Padding', desc: 'Random bytes in front of each message type.', fields: [
    { k: 's1', l: 'S1', h: 'Handshake init' }, { k: 's2', l: 'S2', h: 'Handshake response' }, { k: 's3', l: 'S3', h: 'Cookie reply' }, { k: 's4', l: 'S4', h: 'Data packets' }] },
  { id: 'hdr', title: 'Message types', desc: 'A number or a range like 1000-2000. Not 0, ranges must not overlap.', wide: true, fields: [
    { k: 'h1', l: 'H1', h: 'Handshake init' }, { k: 'h2', l: 'H2', h: 'Handshake response' }, { k: 'h3', l: 'H3', h: 'Cookie reply' }, { k: 'h4', l: 'H4', h: 'Data packets' }] },
  { id: 'extra', title: 'Extra packets', desc: 'Optional, in CPS syntax. Sent after I1.', wide: true, fields: [
    { k: 'i2', l: 'I2', h: '', p: '<r 32>' }, { k: 'i3', l: 'I3', h: '', p: '' }, { k: 'i4', l: 'I4', h: '', p: '' }, { k: 'i5', l: 'I5', h: '', p: '' }] }
]
const OBF_TOGGLES = [
  { k: 'trailers', t: 'Random trailers', d: 'Adds random bytes after packets. Needs equal S1–S4, or single values in H1–H4.' },
  { k: 'no_cookie', t: 'Disable cookies', d: 'Never send cookie replies, even under load.' }
]
const PRESET_DESC = {
  balanced: 'Moderate junk and padding. A good default.',
  stealth: 'More junk, larger padding, wide header ranges. Costs some speed.',
  speed: 'Minimal junk and padding. For networks with light filtering.'
}
const INST_ACTIONS = [
  { id: 'endpoint', t: 'Change endpoint', ri: true }, { id: 'i1', t: 'Change first packet', ri: true },
  { id: 'rotate', t: 'Rotate I1 now', ri: true }, { id: 'obf', t: 'Change obfuscation', ri: true },
  { id: 'ipv6', t: 'Turn IPv6 off', ri: true }, { id: 'mtu', t: 'Change MTU', ri: true },
  { id: 'rate', t: 'Rate limit' },
  { id: 'remove', t: 'Remove instance', danger: true }
]
const SHEET_STEPS = { add: ['basics', 'obf', 'i1', 'review'], obf: ['obf'], i1: ['i1'], endpoint: ['endpoint'] }
const STEP_TITLES = { basics: 'Basics', obf: 'Obfuscation', i1: 'First packet', review: 'Review' }

function rateText(r) { return r ? r + ' Mbit/s' : 'off' }

function issueKeys(field) {
  const f = String(field || '').toLowerCase()
  const m = f.match(/^([a-z])(\d)-[a-z](\d)$/)
  if (m) { const out = []; for (let i = +m[2]; i <= +m[3]; i++) out.push(m[1] + i); return out }
  return [f]
}

AWGX.push(() => ({
  instances: [],
  instLoaded: false,
  presets: { presets: [], custom: {}, has_ipv6: false },
  obfGroups: OBF,
  obfToggles: OBF_TOGGLES,
  mt: { inst: '', auto: true, value: '', busy: false, error: '' },
  rt: { name: '', uplink: false, value: '', busy: false, error: '' },
  is: { mode: 'add', step: 0, inst: '', name: '', port: '', endpoint: '', dns: '', ipv6: false, kind: 'preset', preset: 'balanced', params: {}, i1: 'none', sni: '', cps: '', selftest: true, issues: [], busy: false, error: '', field: '' },

  async load_instances() {
    const [l, p] = await Promise.all([this.api('GET', '/api/instances'), this.presets.presets.length ? null : this.api('GET', '/api/presets')])
    this.instances = l
    this.instLoaded = true
    if (p) this.presets = p
  },
  get instMany() { return this.instances.length > 1 },
  get cur() {
    return this.instances.find(i => i.name === this.sub) || this.instances[0] || { name: '', params: {}, i1: {}, dns: [], extra: [] }
  },
  instTab(i) { return this.cur.name === i.name ? 'page' : false },
  get curUp() { return this.cur.up ? 'up' : 'down' },
  get curUpClass() { return 'badge ' + (this.cur.up ? 'badge-ok' : 'badge-fail') },
  get curPeers() { return plural(this.cur.peers || 0, 'client', 'clients') },
  get curImpact() { return 'Every client of ' + this.cur.name + ' (' + this.curPeers + ') must import its config again.' },
  get curNet() {
    const i = this.cur
    return [
      { k: 'Endpoint', v: i.endpoint + ':' + i.port, c: 'mono', ri: true },
      { k: 'MTU', v: i.mtu + (i.mtu_fixed ? ' (fixed)' : ' (automatic)'), c: '', ri: true },
      { k: 'Rate limit', v: rateText(i.rate), c: '' },
      { k: 'Clients', v: String(i.peers), c: '' },
      { k: 'IPv4 subnet', v: i.subnet4, c: 'mono' },
      { k: 'IPv6 subnet', v: i.subnet6 || 'off', c: 'mono' },
      { k: 'DNS', v: (i.dns || []).join(', '), c: 'mono' }
    ]
  },
  get curI1() {
    const i = this.cur, x = i.i1 || {}
    const r = [{ k: 'Profile', v: x.profile || 'none', c: '' }]
    if (x.sni) r.push({ k: 'SNI', v: x.sni, c: 'mono' })
    r.push({ k: 'Last rotation', v: i.i1_rotated_at ? fmtRel(i.i1_rotated_at) : 'never', c: '' })
    return r
  },
  get curPacket() { const p = this.cur.i1_packet || ''; return p.length > 120 ? p.slice(0, 120) + '…' : p },
  get hasPacket() { return !!this.cur.i1_packet },
  get curObf() {
    const p = this.cur.params || {}
    return OBF.map(g => ({ id: g.id, title: g.title, wide: g.wide, cls: 'pvals' + (g.wide ? ' pvals-wide' : ''), vals: g.fields.map(f => ({ k: f.k, l: f.l, v: p[f.k] === '' || p[f.k] == null ? '—' : p[f.k] })) }))
      .filter(g => g.id !== 'extra' || g.vals.some(v => v.v !== '—'))
  },
  get curFlags() {
    const i = this.cur, p = i.params || {}
    return [
      { k: 'Random trailers', v: p.trailers ? 'on' : 'off' },
      { k: 'Cookies', v: p.no_cookie ? 'disabled' : 'enabled' },
      { k: 'Header protection key', v: i.header_key ? 'set' : 'not set' }
    ].concat((i.extra || []).map(e => ({ k: e[0], v: e[1] })))
  },
  get curPresetClass() { return 'badge ' + (this.cur.preset === 'custom' ? 'badge-warn' : 'badge-brand') },
  get instActions() { return INST_ACTIONS.map(a => a.id === 'ipv6' ? Object.assign({}, a, { t: this.cur.ipv6 ? 'Turn IPv6 off' : 'Turn IPv6 on' }) : a) },
  instAction(a) {
    this.menu = ''
    if (a.id === 'rotate') return this.askRotate()
    if (a.id === 'ipv6') return this.askIPv6()
    if (a.id === 'mtu') return this.openMTU()
    if (a.id === 'rate') return this.openRate(this.cur.name, this.cur.rate, false)
    if (a.id === 'remove') return this.askRemoveInst()
    this.openInstSheet(a.id)
  },
  actClass(a) { return 'menu-item' + (a.danger ? ' menu-danger' : '') },

  async openInstSheet(mode) {
    if (!this.presets.presets.length) this.presets = await this.api('GET', '/api/presets')
    const c = this.cur
    const add = mode === 'add'
    const i1 = add ? { profile: 'noise' } : c.i1 || { profile: 'none' }
    this.is = {
      mode, step: 0, inst: add ? '' : c.name, name: '', port: '', endpoint: add ? '' : c.endpoint, dns: '', ipv6: !!this.presets.has_ipv6,
      kind: !add && c.preset === 'custom' ? 'custom' : 'preset', preset: add ? 'balanced' : c.preset === 'custom' ? 'balanced' : c.preset,
      params: Object.assign({}, add ? this.presets.custom : c.params), i1: i1.profile, sni: i1.sni || '', cps: i1.custom || '', selftest: true, issues: [], busy: false, error: '', field: ''
    }
    this.openDialog('instDlg')
  },
  addInstance() { this.openInstSheet('add') },
  get isSteps() { return SHEET_STEPS[this.is.mode] },
  get isStep() { return this.isSteps[this.is.step] },
  stepIs(s) { return this.isStep === s },
  get isMulti() { return this.isSteps.length > 1 },
  get isStepper() { return this.isSteps.map((s, i) => ({ id: s, n: i + 1, t: STEP_TITLES[s], state: i < this.is.step ? 'done' : i === this.is.step ? 'current' : 'todo' })) },
  get isTitle() {
    return { add: 'Add instance', obf: 'Obfuscation: ' + this.is.inst, i1: 'First packet: ' + this.is.inst, endpoint: 'Endpoint: ' + this.is.inst }[this.is.mode]
  },
  get isDesc() {
    if (this.is.mode === 'add') return 'A new interface with its own port, subnets and obfuscation. It is created as a background task.'
    return 'Changes client configs of ' + this.is.inst + '.'
  },
  get isWide() { return this.isStep === 'obf' ? 'sheet sheet-wide' : 'sheet' },
  get isFirst() { return this.is.step === 0 },
  get isNotFirst() { return this.is.step > 0 },
  get isLast() { return this.is.step === this.isSteps.length - 1 },
  get isEdit() { return this.is.mode !== 'add' },
  get isSubmit() {
    if (this.is.mode === 'add') return 'Create instance'
    return 'Save and re-issue ' + plural(this.cur.peers || 0, 'config', 'configs')
  },
  isPrimary() { if (this.isLast) this.isSave(); else this.isNext() },
  get isPrimaryLabel() { return this.isLast ? this.isSubmit : 'Continue' },
  isBack() { if (this.is.step > 0) this.is.step-- },
  isNext() {
    const s = this.isStep
    this.is.error = ''
    this.is.field = ''
    if (s === 'basics') {
      if (!INSTANCE_RE.test(this.is.name)) { this.is.field = 'name'; this.is.error = this.is.name ? 'Use 1-13 lowercase letters, digits, - or _, starting with a letter' : 'Name is required'; return }
      if (this.instances.some(i => i.name === this.is.name)) { this.is.field = 'name'; this.is.error = 'An instance named ' + this.is.name + ' already exists'; return }
      if (this.is.port && !(/^\d+$/.test(this.is.port) && +this.is.port > 0 && +this.is.port < 65536)) { this.is.field = 'port'; this.is.error = 'Enter a UDP port from 1 to 65535'; return }
    }
    if (s === 'i1' && this.is.i1 === 'quic' && !this.is.sni.trim()) { this.is.field = 'sni'; this.is.error = 'quic needs a server name'; return }
    this.is.step++
    this.$nextTick(() => { const b = this.$refs.instDlg.querySelector('.sheet-body'); if (b) b.scrollTop = 0 })
  },
  isErr(f) { return this.is.field === f ? this.is.error : '' },
  isInvalid(f) { return this.is.field === f ? 'true' : 'false' },
  get isGeneralError() { return this.is.field ? '' : this.is.error },
  get presetList() { return this.presets.presets.map(n => ({ name: n, desc: PRESET_DESC[n] || '' })) },
  presetChecked(p) { return this.is.preset === p.name ? 'true' : 'false' },
  pickPreset(p) { this.is.preset = p.name },
  kindChecked(k) { return this.is.kind === k ? 'true' : 'false' },
  setKind(k) { this.is.kind = k },
  get isCustom() { return this.is.kind === 'custom' },
  get isPreset() { return this.is.kind === 'preset' },
  obfId(f) { return 'obf-' + f.k },
  obfVal(f) { return this.is.params[f.k] },
  obfInput(e) { this.is.params[e.target.dataset.k] = e.target.value },
  obfIssue(f) { return this.is.issues.find(x => issueKeys(x.field).includes(f.k)) },
  obfInvalid(f) { const x = this.obfIssue(f); return x && x.level === 'fail' ? 'true' : 'false' },
  obfFieldClass(f) { const x = this.obfIssue(f); return 'field' + (x && x.level === 'warn' ? ' field-warn' : '') },
  obfMsg(f) { const x = this.obfIssue(f); return x ? x.message : f.h },
  obfMsgClass(f) { const x = this.obfIssue(f); return x ? (x.level === 'fail' ? 'error' : 'warn-text small') : 'hint' },
  obfDesc(f) { return 'obf-' + f.k + '-msg' },
  obfGroupClass(g) { return 'fgrid' + (g.wide ? ' fgrid-wide' : '') },
  toggleOn(t) { return this.is.params[t.k] ? 'true' : 'false' },
  flipToggle(t) { this.is.params[t.k] = !this.is.params[t.k] },
  toggleId(t) { return 'tg-' + t.k },
  get issueFails() { return this.is.issues.filter(x => x.level === 'fail') },
  get issueWarns() { return this.is.issues.filter(x => x.level !== 'fail') },
  get hasFails() { return this.issueFails.length > 0 },
  get hasWarns() { return this.issueWarns.length > 0 },
  get failTitle() { return plural(this.issueFails.length, 'problem blocks saving', 'problems block saving') },
  focusIssue(x) {
    const k = issueKeys(x.field)[0]
    const el = document.getElementById('obf-' + k) || document.getElementById('i1-' + k)
    if (el) el.focus()
  },
  i1Checked(o) { return this.is.i1 === o.v ? 'true' : 'false' },
  pickI1(o) { this.is.i1 = o.v },
  get isQuic() { return this.is.i1 === 'quic' },
  get isCps() { return this.is.i1 === 'custom' },
  get selftestOn() { return this.is.selftest ? 'true' : 'false' },
  flipSelftest() { this.is.selftest = !this.is.selftest },
  get isIPv6On() { return this.is.ipv6 ? 'true' : 'false' },
  flipIsIPv6() { this.is.ipv6 = !this.is.ipv6 },
  get noIPv6Route() { return !this.presets.has_ipv6 },
  get isReview() {
    const s = this.is
    const base = this.instances.find(i => i.name === 'awg0') || (this.instances.length === 1 ? this.instances[0] : null)
    return [
      { k: 'Name', v: s.name },
      { k: 'UDP port', v: s.port || 'random' },
      { k: 'Endpoint', v: s.endpoint || (base ? base.endpoint + ' (from ' + base.name + ')' : 'detected from the local address') },
      { k: 'DNS', v: s.dns || 'default' },
      { k: 'IPv6', v: s.ipv6 ? 'on' : 'off' },
      { k: 'Obfuscation', v: s.kind === 'custom' ? 'custom values' : 'preset ' + s.preset },
      { k: 'First packet', v: s.i1 + (s.i1 === 'quic' ? ' · ' + s.sni : '') }
    ]
  },
  async isSave() {
    const s = this.is
    s.error = ''
    s.field = ''
    s.busy = true
    const i1 = { i1: s.i1, sni: s.sni.trim(), cps: s.cps.trim() }
    const params = s.kind === 'custom' ? s.params : {}
    const preset = s.kind === 'custom' ? 'custom' : s.preset
    try {
      if (s.mode === 'add') {
        const body = Object.assign({ name: s.name, port: s.port ? +s.port : 0, endpoint: s.endpoint.trim(), dns: s.dns.split(',').map(x => x.trim()).filter(Boolean), preset, params, ipv6: s.ipv6, selftest: s.selftest }, i1)
        const name = s.name
        await this.startJob(this.api('POST', '/api/instances', body), function (j) { if (!j.error) location.hash = '#/instances/' + name })
        this.closeDialog('instDlg')
        return
      }
      if (s.mode === 'obf') {
        const r = await this.api('PUT', '/api/instances/' + enc(s.inst) + '/obfuscation', { preset, params })
        const w = (r && r.issues) || []
        this.toast(w.length ? 'warn' : 'ok', w.length ? 'Saved with ' + plural(w.length, 'warning', 'warnings') + ': ' + w[0].message : 'Obfuscation saved. Send the new configs to clients.')
      }
      if (s.mode === 'i1') { await this.api('PUT', '/api/instances/' + enc(s.inst) + '/i1', i1); this.toast('ok', 'First packet saved. Send the new configs to clients.') }
      if (s.mode === 'endpoint') { await this.api('PUT', '/api/instances/' + enc(s.inst) + '/endpoint', { endpoint: s.endpoint.trim() }); this.toast('ok', 'Endpoint saved. Send the new configs to clients.') }
      this.closeDialog('instDlg')
      await this.refresh(true)
    } catch (e) {
      if (e.data.issues) {
        s.issues = e.data.issues
        s.error = ''
        if (s.mode === 'add') { s.step = 1; s.kind = 'custom' }
        this.$nextTick(() => { const b = this.$refs.instDlg.querySelector('.sheet-body'); if (b) b.scrollTop = 0 })
      } else {
        let f = e.data.field || ''
        if (f === 'i1') f = s.i1 === 'quic' ? 'sni' : ''
        if (f === 'dns') f = ''
        s.error = e.message
        s.field = f
        const st = this.isSteps.indexOf(f === 'sni' ? 'i1' : 'basics')
        if (f && st >= 0) s.step = st
      }
    } finally { s.busy = false }
  },
  askRotate() {
    const c = this.cur
    this.ask({
      title: 'Rotate I1 on ' + c.name + '?', text: 'A new first packet is generated. Old configs keep working until clients import the new ones.',
      impact: plural(c.peers, 'client gets', 'clients get') + ' a new config.', label: 'Rotate now',
      async run() {
        const r = await this.api('POST', '/api/instances/' + enc(c.name) + '/i1/rotate')
        this.toast('ok', 'I1 rotated: ' + plural(r.affected.length, 'client', 'clients') + ' can import the new config')
        await this.refresh(true)
      }
    })
  },
  openMTU() {
    const c = this.cur
    this.mt = { inst: c.name, auto: !c.mtu_fixed, value: String(c.mtu || ''), busy: false, error: '' }
    this.openDialog('mtuDlg')
  },
  get mtAutoText() { return 'Computed from this server\'s link MTU minus 60 bytes and S4: ' + (this.cur.mtu_auto || '') },
  get mtAutoOn() { return this.mt.auto ? 'true' : 'false' },
  flipMtAuto() { this.mt.auto = !this.mt.auto },
  get mtHolder() { return String(this.cur.mtu_auto || '') },
  get mtManual() { return !this.mt.auto },
  get mtInvalid() { return this.mt.error ? 'true' : 'false' },
  async saveMTU() {
    const f = this.mt
    f.error = ''
    let mtu = 0
    if (!f.auto) {
      mtu = Number(f.value)
      if (!/^\d+$/.test(f.value)) { f.error = 'Enter a number'; return }
    }
    f.busy = true
    try {
      await this.api('PUT', '/api/instances/' + enc(f.inst) + '/mtu', { mtu })
      this.closeDialog('mtuDlg')
      this.toast('ok', 'MTU saved. Send the new configs to clients.')
      await this.refresh(true)
    } catch (e) { f.error = e.message } finally { f.busy = false }
  },
  openRate(name, rate, uplink) {
    this.rt = { name, uplink, value: rate ? String(rate) : '', busy: false, error: '' }
    this.openDialog('rateDlg')
  },
  get rtWhat() { return this.rt.uplink ? 'Shapes traffic this server sends to the other server (upload). Download is limited by the rate on the other server.' : 'Shapes traffic this server sends to clients (their download).' },
  get rtInvalid() { return this.rt.error ? 'true' : 'false' },
  async saveRate() {
    const f = this.rt
    f.error = ''
    const v = String(f.value).trim()
    const rate = v ? Number(v) : 0
    if (v && !/^\d+$/.test(v)) { f.error = 'A number of Mbit/s, or empty for no limit'; return }
    f.busy = true
    try {
      await this.api('PUT', '/api/instances/' + enc(f.name) + '/rate', { rate })
      this.closeDialog('rateDlg')
      this.toast('ok', 'Rate limit of ' + f.name + ': ' + rateText(rate))
      if (f.uplink) await this.load_egress()
      else await this.refresh(true)
    } catch (e) { f.error = e.message } finally { f.busy = false }
  },
  askIPv6() {
    const c = this.cur, on = !c.ipv6
    this.ask({
      title: 'Turn IPv6 ' + (on ? 'on' : 'off') + ' on ' + c.name + '?',
      text: on ? 'Clients get IPv6 addresses inside the tunnel. The interface restarts.' : 'Clients get IPv4 only. IPv6 traffic is still captured by the tunnel, so nothing leaks. The interface restarts.',
      impact: 'Every client of ' + c.name + ' (' + this.curPeers + ') must import its config again.', label: on ? 'Turn IPv6 on' : 'Turn IPv6 off',
      async run() {
        await this.api('POST', '/api/instances/' + enc(c.name) + '/ipv6', { on })
        this.toast('ok', 'IPv6 turned ' + (on ? 'on' : 'off') + '. Send the new configs to clients.')
        await this.refresh(true)
      }
    })
  },
  askRemoveInst() {
    const c = this.cur
    this.ask({
      title: 'Remove instance ' + c.name + '?', text: 'The interface, its ' + this.curPeers + ' and their routes are deleted.',
      typed: c.name, label: 'Remove instance', danger: true,
      async run() {
        await this.api('DELETE', '/api/instances/' + enc(c.name), { confirm: c.name })
        this.toast('ok', c.name + ' removed')
        location.hash = '#/instances'
        await this.refresh(true)
      }
    })
  }
}))
