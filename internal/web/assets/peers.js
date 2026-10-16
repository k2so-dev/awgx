const I1_OPTS = [
  { v: 'none', t: 'none', d: 'No imitation packet' },
  { v: 'noise', t: 'noise', d: 'Random bytes' },
  { v: 'quic', t: 'quic', d: 'QUIC Initial with an SNI' },
  { v: 'stun', t: 'stun', d: 'STUN binding request' },
  { v: 'dns', t: 'dns', d: 'DNS query' },
  { v: 'custom', t: 'custom', d: 'Your own CPS string' }
]
const EXPIRY = [{ v: 'keep', t: 'Keep' }, { v: 'never', t: 'Never' }, { v: '30d', t: '30 days' }, { v: '90d', t: '90 days' }, { v: 'custom', t: 'Custom' }]

function i1Text(i) {
  if (!i) return 'inherit'
  return i.profile + (i.sni ? ' · ' + i.sni : '') + (i.custom ? ' · custom CPS' : '')
}

AWGX.push(() => ({
  peers: [],
  peersLoaded: false,
  q: '',
  fInstance: '',
  fStatus: '',
  i1Opts: I1_OPTS,
  expiryOpts: EXPIRY,
  form: { name: '', instance: '', i1: '', sni: '', cps: '', days: '', note: '', egress: 'inherit', net: '' },
  formTouched: false,
  formBusy: false,
  formError: '',
  formField: '',
  exp: null,
  expName: '',
  expTab: 'qr',
  pe: { name: '', note: '', net: '', expiry: 'keep', custom: '', i1: 'keep', sni: '', cps: '', egress: 'keep', ks: false, busy: false, error: '' },

  async load_peers(egress = true) {
    const [v, e] = await Promise.all([this.api('GET', '/api/overview?rows=1'), egress && !this.egressLoaded ? this.api('GET', '/api/egress') : null])
    this.setOverview(v)
    this.peers = v.rows
    this.peersLoaded = true
    if (e) { this.egress = e; this.egressLoaded = true }
  },
  get hasPeers() { return this.peers.length > 0 },
  get peersEmpty() { return this.peersLoaded && this.peers.length === 0 },
  dotClass(p) { return p.online ? 'dot dot-on' : 'dot' },
  dotLabel(p) { return p.online ? 'Online' : 'Offline' },
  statusClass(p) { return 'badge ' + (p.status === 'expired' ? 'badge-warn' : p.status === 'disabled' ? 'badge-muted' : 'badge-ok') },
  expiryText(p) {
    if (!p.expires_at) return ''
    return (p.status === 'expired' ? 'expired ' : 'expires ') + fmtDate(Date.parse(p.expires_at))
  },
  egressText(p) { return p.egress || 'default' },
  egressClass(p) { return p.egress ? 'badge badge-mono' : 'small muted' },
  get filteredPeers() {
    const q = this.q.trim().toLowerCase()
    return this.peers.filter(p => {
      if (this.fInstance && p.instance !== this.fInstance) return false
      if (this.fStatus === 'online' && !p.online) return false
      if (this.fStatus && this.fStatus !== 'online' && p.status !== this.fStatus) return false
      if (!q) return true
      return [p.name, p.address4, p.address6, p.note || '', p.net_label || ''].some(v => v.toLowerCase().includes(q))
    }).sort((a, b) => a.instance.localeCompare(b.instance) || a.name.localeCompare(b.name))
  },
  get noMatches() { return this.peers.length > 0 && this.filteredPeers.length === 0 },
  get hasFilters() { return !!(this.q || this.fInstance || this.fStatus) },
  get peerCount() {
    const n = this.filteredPeers.length, t = this.peers.length
    return n === t ? plural(t, 'peer', 'peers') : n + ' of ' + t + ' peers'
  },
  clearFilters() { this.q = ''; this.fInstance = ''; this.fStatus = ''; if (this.sub) location.hash = '#/peers' },
  rowKey(e, p) { if (e.key === 'Enter') { e.preventDefault(); this.openPeer(p) } },

  openAdd() {
    this.form = { name: '', instance: this.instanceNames[0] || '', i1: '', sni: '', cps: '', days: '', note: '', egress: 'inherit', net: '' }
    this.formTouched = false
    this.formError = ''
    this.formField = ''
    this.openDialog('addDlg')
  },
  touchName() { if (this.form.name) this.formTouched = true },
  get nameError() {
    const n = this.form.name
    if (this.formField === 'name') return this.formError
    if (!this.formTouched) return ''
    if (!n) return 'Name is required'
    if (n.length > 32) return 'Use at most 32 characters'
    if (!PEER_RE.test(n)) return 'Use latin letters, digits and - only'
    if (this.peers.some(p => p.name === n)) return 'A peer named ' + n + ' already exists'
    return ''
  },
  get nameOk() { return !this.nameError },
  get nameInvalid() { return this.nameError ? 'true' : 'false' },
  get daysError() {
    const d = String(this.form.days).trim()
    if (!d) return ''
    if (!/^\d+$/.test(d) || +d < 1) return 'Enter a whole number of days'
    return this.formField === 'days' ? this.formError : ''
  },
  get daysInvalid() { return this.daysError ? 'true' : 'false' },
  get formGeneralError() { return ['name', 'days'].includes(this.formField) ? '' : this.formError },
  get addQuic() { return this.form.i1 === 'quic' },
  get addCustom() { return this.form.i1 === 'custom' },
  instI1(name) { const i = this.overview.instances.find(x => x.name === name) || this.overview.instances[0]; return i ? i.i1 : 'none' },
  get inheritLabel() { return 'Inherit from instance (' + this.instI1(this.form.instance) + ')' },
  async submitAdd() {
    this.formTouched = true
    this.formError = ''
    this.formField = ''
    if (this.nameError || this.daysError) {
      this.$nextTick(() => { const el = this.$refs.addDlg.querySelector('[aria-invalid="true"]'); if (el) el.focus() })
      return
    }
    this.formBusy = true
    try {
      const f = this.form
      const peer = await this.api('POST', '/api/peers', { name: f.name, instance: f.instance, days: f.days ? +f.days : 0, i1: f.i1, sni: f.sni.trim(), cps: f.cps.trim(), note: f.note.trim(), egress: f.egress, net: f.net.trim() })
      this.closeDialog('addDlg')
      const w = peer.issues || []
      this.toast(w.length ? 'warn' : 'ok', 'Peer ' + peer.name + ' created' + (w.length ? ' with ' + plural(w.length, 'warning', 'warnings') + ': ' + w[0].message : ''))
      if (f.egress !== 'inherit') {
        this.egressLoaded = false
        this.toast('ok', 'The route of ' + peer.name + ' is in the egress draft.', '#/egress/routes', 'Review')
      }
      await this.refresh(true)
      this.$nextTick(() => this.openExport(peer))
    } catch (e) {
      this.formError = e.message
      this.formField = e.data.field || ''
    } finally { this.formBusy = false }
  },

  async openExport(p) {
    this.exp = null
    this.expName = p.name
    this.expTab = 'qr'
    this.openDialog('exportDlg')
    try { this.exp = await this.api('GET', '/api/peers/' + enc(p.name) + '/export') } catch (e) {
      this.closeDialog('exportDlg')
      this.toast('fail', e.message)
    }
  },
  expPane(t) { return !!this.exp && this.expTab === t },
  get expLoading() { return !this.exp },
  get expQr() { return this.exp ? this.exp.qr : [] },
  get expMany() { return this.expQr.length > 1 },
  get expConf() { return this.exp ? this.exp.conf : '' },
  get expVpn() { return this.exp ? this.exp.vpn : '' },
  get expTitle() { return 'Client config: ' + this.expName },
  get qrGridClass() { return 'qr-grid' + (this.expMany ? ' qr-many' : '') },
  qrAlt(i) { return 'QR code ' + (i + 1) + ' of ' + this.expQr.length + ' for ' + this.expName },
  qrCap(i) { return (i + 1) + ' of ' + this.expQr.length },
  tabSel(t) { return this.expTab === t ? 'true' : 'false' },
  tabIndex(t) { return this.expTab === t ? '0' : '-1' },
  setTab(t) { this.expTab = t },
  tabKey(e) {
    const tabs = ['qr', 'conf', 'vpn']
    let i = tabs.indexOf(this.expTab)
    if (e.key === 'ArrowRight') i = (i + 1) % 3
    else if (e.key === 'ArrowLeft') i = (i + 2) % 3
    else if (e.key === 'Home') i = 0
    else if (e.key === 'End') i = 2
    else return
    e.preventDefault()
    this.expTab = tabs[i]
    this.$nextTick(() => e.currentTarget.querySelector('[aria-selected="true"]').focus())
  },
  download(name, text) {
    const a = document.createElement('a')
    a.href = URL.createObjectURL(new Blob([text], { type: 'application/octet-stream' }))
    a.download = name
    document.body.appendChild(a)
    a.click()
    a.remove()
    setTimeout(() => URL.revokeObjectURL(a.href), 1000)
  },
  downloadConf() { this.download(this.expName + '.conf', this.expConf); this.toast('ok', this.expName + '.conf downloaded') },
  copyVpn() { this.copy(this.expVpn, 'vpn:// link') },

  addr6(p) { return p.address6 || 'IPv6 off' },
  get peSel() { return this.peers.find(p => p.name === this.pe.name) || { name: this.pe.name, address4: '', address6: '', instance: '' } },
  openPeer(p) {
    this.pe = { name: p.name, note: p.note || '', net: p.net_label || '', expiry: 'keep', custom: '', i1: 'keep', sni: '', cps: (p.i1 && p.i1.custom) || '', egress: 'keep', ks: !!p.killswitch, busy: false, error: '' }
    this.openDialog('peerDlg')
  },
  get peRows() {
    const p = this.peSel
    const rows = [
      { k: 'Addresses', v: p.address4 + '\n' + (p.address6 || 'IPv6 off'), c: 'mono pre' },
      { k: 'Last handshake', v: fmtRel(p.last_handshake), c: '' },
      { k: 'Endpoint', v: p.endpoint || '—', c: 'mono' },
      { k: 'Transfer', v: '↓ ' + fmtBytes(p.rx_bytes) + '  ↑ ' + fmtBytes(p.tx_bytes), c: '' },
      { k: 'Created', v: p.created_at ? fmtAbs(Date.parse(p.created_at)) : '', c: '' },
      { k: 'Public key', v: p.public_key || '', c: 'mono break' }
    ]
    if (this.multi) rows.unshift({ k: 'Instance', v: p.instance, c: '' })
    return rows
  },
  get peTitle() { return this.pe.name },
  get peStatus() { return this.peSel.status || '' },
  get peStatusClass() { return this.statusClass(this.peSel) },
  get peExpiryNow() {
    const p = this.peSel
    if (!p.expires_at) return 'Never expires'
    return (p.status === 'expired' ? 'Expired ' : 'Expires ') + fmtDate(Date.parse(p.expires_at)) + ', ' + new Date(Date.parse(p.expires_at)).getFullYear() + ' (' + fmtRel(p.expires_at) + ')'
  },
  expiryChecked(o) { return this.pe.expiry === o.v ? 'true' : 'false' },
  setExpiry(o) { this.pe.expiry = o.v; if (o.v === 'custom') this.$nextTick(() => this.$refs.peCustom.focus()) },
  get peCustomShown() { return this.pe.expiry === 'custom' },
  get peCustomError() { return this.pe.expiry === 'custom' && this.pe.custom && !/^\d+[dh]$/.test(this.pe.custom.trim()) ? 'Use a number with d or h, like 45d or 12h' : '' },
  get peCustomInvalid() { return this.peCustomError ? 'true' : 'false' },
  get peI1Now() { return 'Now: ' + (this.peSel.i1 ? i1Text(this.peSel.i1) : 'inherited from ' + this.peSel.instance + ' (' + this.instI1(this.peSel.instance) + ')') },
  get peQuic() { return this.pe.i1 === 'quic' },
  get peSniHint() { const s = this.peSel.i1 && this.peSel.i1.sni; return s ? 'keep ' + s : 'the instance SNI' },
  get peCustom() { return this.pe.i1 === 'custom' },
  get peI1Changed() { return this.pe.i1 !== 'keep' },
  get peImpact() { return this.pe.name + ' must import its config again after this change.' },
  get peEgressNow() {
    const p = this.peSel
    const d = this.egress.default && this.egress.default[p.instance]
    return 'Now: ' + (p.egress ? p.egress + (p.killswitch ? ' with killswitch' : '') : 'instance default (' + (d ? d.profile : 'direct') + ')')
  },
  get egressChoices() { return (this.egress.profiles || []).map(p => p.name) },
  get peEgressChanged() { return this.pe.egress !== 'keep' || this.pe.ks !== !!this.peSel.killswitch },
  get peKsAllowed() {
    const v = this.pe.egress === 'keep' ? this.peSel.egress || 'inherit' : this.pe.egress
    return v !== 'direct' && v !== 'inherit'
  },
  get peKsDisabled() { return !this.peKsAllowed },
  get peKs() { return this.pe.ks && this.peKsAllowed ? 'true' : 'false' },
  togglePeKs() { if (this.peKsAllowed) this.pe.ks = !this.pe.ks },
  get peToggleLabel() { return this.peSel.enabled ? 'Disable peer' : 'Enable peer' },
  get peToggleDesc() { return this.peSel.enabled ? 'The client cannot connect until you enable it again. Keys and address are kept.' : 'The client can connect again.' },
  async savePeer() {
    const p = this.peSel, f = this.pe
    if (this.peCustomError) return
    const body = {}
    if (f.note !== (p.note || '')) body.note = f.note.trim()
    if (f.net !== (p.net_label || '')) body.net = f.net.trim()
    if (f.expiry === 'custom' && f.custom.trim()) body.expires_in = f.custom.trim()
    else if (f.expiry !== 'keep' && f.expiry !== 'custom') body.expires_in = f.expiry
    if (f.i1 !== 'keep') { body.i1 = f.i1; body.sni = f.sni.trim(); body.cps = f.cps.trim() }
    const eg = this.peEgressChanged
    if (!Object.keys(body).length && !eg) { this.closeDialog('peerDlg'); return }
    f.busy = true
    f.error = ''
    try {
      if (eg) await this.api('PUT', '/api/peers/' + enc(p.name) + '/egress', { profile: f.egress === 'keep' ? p.egress || 'inherit' : f.egress, killswitch: f.ks && this.peKsAllowed })
      const r = Object.keys(body).length ? await this.api('PATCH', '/api/peers/' + enc(p.name), body) : null
      const w = (r && r.issues) || []
      this.closeDialog('peerDlg')
      this.egressLoaded = false
      await this.refresh(true)
      if (eg) this.toast('ok', 'Saved. The route change is in the egress draft.', '#/egress/routes', 'Review')
      else if (w.length) this.toast('warn', 'Saved with ' + plural(w.length, 'warning', 'warnings') + ': ' + w[0].message)
      else if (body.i1) this.toast('ok', 'Saved. Send the new config to ' + p.name + '.')
      else this.toast('ok', 'Peer ' + p.name + ' saved')
      if (body.i1) this.$nextTick(() => this.openExport(p))
    } catch (e) { f.error = e.message } finally { f.busy = false }
  },
  async togglePeer(p) {
    const action = p.enabled ? 'disable' : 'enable'
    try {
      await this.api('POST', '/api/peers/' + enc(p.name) + '/' + action)
      this.toast('ok', 'Peer ' + p.name + (p.enabled ? ' disabled' : ' enabled'))
      await this.refresh(true)
    } catch (e) { this.toast('fail', e.message) }
  },
  togglePe() { this.togglePeer(this.peSel) },
  exportPe() { this.openExport(this.peSel) },
  askDelete(p) {
    this.ask({
      title: 'Delete peer ' + p.name + '?', text: 'Its client stops working immediately. This cannot be undone.', label: 'Delete peer', danger: true,
      async run() {
        await this.api('DELETE', '/api/peers/' + enc(p.name))
        this.closeDialog('peerDlg')
        this.toast('ok', 'Peer ' + p.name + ' deleted')
        await this.refresh(true)
      }
    })
  },
  deletePe() { this.askDelete(this.peSel) },
  editLabel(p) { return 'Edit ' + p.name },
  configLabel(p) { return 'Client config for ' + p.name }
}))
