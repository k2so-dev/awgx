const AWGX = []
const clock = () => Date.now()
const enc = encodeURIComponent
const ROW_ROUTES = ['dashboard', 'peers', 'stats']
const STATIC_ROUTES = ['system', 'doctor', 'journal', 'instances']
let refreshRun = null
let refreshNext = null
let refreshLoud = false
const ROUTES = ['dashboard', 'peers', 'stats', 'instances', 'egress', 'doctor', 'system', 'journal']
const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']
const PEER_RE = /^[a-zA-Z0-9-]{1,32}$/
const INSTANCE_RE = /^[a-z][a-z0-9_-]{0,12}$/
const PROFILE_RE = /^[a-z][a-z0-9-]{0,30}$/
const RESERVED_PROFILES = ['direct', 'inherit', 'drop']
const NAV = [
  { id: 'g0', label: '', items: [{ r: 'dashboard', t: 'Dashboard', i: 'i-grid' }] },
  { id: 'g1', label: 'Clients', items: [{ r: 'peers', t: 'Peers', i: 'i-users' }, { r: 'stats', t: 'Stats', i: 'i-chart' }] },
  { id: 'g2', label: 'Server', items: [{ r: 'instances', t: 'Instances', i: 'i-layers' }, { r: 'egress', t: 'Egress', i: 'i-route' }, { r: 'doctor', t: 'Doctor', i: 'i-pulse' }] },
  { id: 'g3', label: 'Admin', items: [{ r: 'system', t: 'System', i: 'i-gear' }, { r: 'journal', t: 'Journal', i: 'i-scroll' }] }
]
const JOBS = {
  'instance.add': 'Add instance', 'doctor.fix': 'Apply fixes', 'xui.connect': 'Connect 3x-ui', 'xui.disconnect': 'Disconnect 3x-ui',
  'update.self': 'Update awgx', 'update.modules': 'Upgrade module and tools', setup: 'Setup', 'uplink.speed': 'Uplink speed test'
}

function pad(n) { return String(n).padStart(2, '0') }
function fmtBytes(v) {
  v = Number(v) || 0
  if (v < 1024) return Math.round(v) + ' B'
  const u = ['KiB', 'MiB', 'GiB', 'TiB']
  let i = -1
  do { v /= 1024; i++ } while (v >= 1024 && i < u.length - 1)
  return (v < 100 ? v.toFixed(1).replace(/\.0$/, '') : Math.round(v)) + ' ' + u[i]
}
function niceMax(m) {
  const unit = m < 1024 ? 1 : Math.pow(1024, Math.floor(Math.log(m) / Math.log(1024)))
  const v = m / unit / 4
  const steps = [0.25, 0.5, 1, 2, 2.5, 5, 10, 20, 25, 50, 100, 200, 250, 500, 1000]
  return (steps.find(s => s >= v) || 1000) * unit * 4
}
function fmtTime(t, sec) { const d = new Date(t); return pad(d.getHours()) + ':' + pad(d.getMinutes()) + (sec ? ':' + pad(d.getSeconds()) : '') }
function fmtDate(t) { const d = new Date(t); return MONTHS[d.getMonth()] + ' ' + d.getDate() }
function fmtAbs(t) { const d = new Date(t); return d.getFullYear() + '-' + pad(d.getMonth() + 1) + '-' + pad(d.getDate()) + ' ' + fmtTime(t, true) }
function fmtRel(ts) {
  if (!ts) return 'never'
  const s = Math.round((clock() - Date.parse(ts)) / 1000)
  if (s < 0) {
    const f = -s
    if (f < 3600) return 'in ' + Math.max(1, Math.floor(f / 60)) + ' min'
    if (f < 86400) return 'in ' + Math.floor(f / 3600) + ' h'
    return 'in ' + Math.floor(f / 86400) + ' d'
  }
  if (s < 10) return 'just now'
  if (s < 60) return s + ' s ago'
  if (s < 3600) return Math.floor(s / 60) + ' min ago'
  if (s < 86400) return Math.floor(s / 3600) + ' h ago'
  return Math.floor(s / 86400) + ' d ago'
}
function plural(n, one, many) { return n + ' ' + (n === 1 ? one : many) }
function checkText(c) { return typeof c === 'string' ? c : (c.status + '   ').slice(0, 5) + (c.profile ? c.profile + ': ' : '') + c.message }
function diffLines(list) {
  return (list || []).map(checkText).map((t, i) => {
    const l = t.toLowerCase()
    return { id: i, text: t, cls: 'diff-line' + (t[0] === '+' ? ' diff-add' : t[0] === '-' ? ' diff-del' : l.startsWith('ok') ? ' diff-ok' : l.startsWith('warn') ? ' diff-warn' : l.startsWith('fail') ? ' diff-fail' : '') }
  })
}

const theme = {
  get() { try { return localStorage.getItem('awgx-theme') || 'system' } catch (e) { return 'system' } },
  set(v) { try { localStorage.setItem('awgx-theme', v) } catch (e) {} this.apply(v) },
  apply(v) { const c = document.documentElement.classList; c.toggle('dark', v === 'dark'); c.toggle('light', v === 'light') }
}
theme.apply(theme.get())

document.addEventListener('alpine:init', () => {
  Alpine.data('app', () => {
    const o = {}
    for (const m of AWGX) Object.defineProperties(o, Object.getOwnPropertyDescriptors(m()))
    return o
  })
})

AWGX.push(() => ({
  booted: false,
  authed: false,
  totp: false,
  csrf: '',
  login: { password: '', code: '', error: '', busy: false },
  loginNotice: '',
  route: 'dashboard',
  sub: '',
  nav: NAV,
  navOpen: false,
  themeMode: theme.get(),
  overview: { online: 0, peers: 0, rx: 0, tx: 0, instances: [], update: '', server: { hostname: '', version: '' } },
  toasts: [],
  busyCount: 0,
  now: Date.now(),
  pollN: 0,
  updateDismissed: '',
  focusBack: {},
  menu: '',
  menuBtn: null,
  restarting: false,
  restartMsg: '',
  restartUnits: '',
  cf: { title: '', text: '', impact: '', typed: '', input: '', label: '', danger: false, busy: false, error: '', run: null },
  job: null,
  jobOpen: false,
  jobSeen: 0,
  jobTimer: 0,
  jobHooks: {},
  doctor: { ran_at: '', checks: [] },
  doctorError: '',
  doctorLoading: false,
  restartFailed: false,
  journal: [],
  jFilter: '',
  stats: { series: [], peers: [] },
  dashSeries: [],
  dashPeriod: '24h',
  statsPeriod: '24h',
  statsPeer: '',
  chartW: 720,
  hover: -1,
  ch: { viewBox: '0 0 720 240', H: 240, yLabelX: 0, xLabelY: 0, bottom: 0, rxLine: '', txLine: '', rxArea: '', txArea: '', grid: '', base: '', yTicks: [{}, {}, {}, {}, {}], xTicks: [{}, {}, {}, {}, {}, {}], L: 0, T: 0, pw: 0, ph: 0, n: 0, empty: true },
  periods: [{ id: '1h', label: '1h' }, { id: '24h', label: '24h' }, { id: '168h', label: '7d' }, { id: '720h', label: '30d' }],

  async init() {
    try {
      this.updateDismissed = localStorage.getItem('awgx-update-dismissed') || ''
      this.jobSeen = Number(localStorage.getItem('awgx-job-seen')) || 0
    } catch (e) {}
    window.addEventListener('hashchange', () => this.syncRoute())
    this.syncRoute(true)
    setInterval(() => this.tick(), 1000)
    setInterval(() => { if (!document.hidden) this.refresh(true, true) }, 5000)
    document.addEventListener('visibilitychange', () => { if (!document.hidden) this.refresh(true) })
    this.$nextTick(() => this.observeCharts())
    try {
      const s = await this.api('GET', '/api/session')
      this.totp = s.totp
      this.csrf = s.csrf
      this.authed = s.authenticated
      if (this.authed) { await this.refresh(); this.pollJob() }
    } catch (e) { this.toast('fail', e.message) }
    this.booted = true
  },

  async api(method, path, body) {
    const mutating = method !== 'GET'
    if (mutating) this.busyCount++
    try {
      const headers = { Accept: 'application/json' }
      if (mutating) headers['X-CSRF-Token'] = this.csrf
      if (body) headers['Content-Type'] = 'application/json'
      const res = await fetch(path, { method, headers, body: body ? JSON.stringify(body) : undefined, credentials: 'same-origin' })
        .catch(() => { throw Object.assign(new Error('Server is not reachable'), { status: 0, data: {} }) })
      const text = await res.text()
      const status = res.status
      let data = null
      try { data = text ? JSON.parse(text) : null } catch (e) {
        if (res.ok) throw Object.assign(new Error('Bad response from the server'), { status, data: {} })
      }
      if (!res.ok && !data) data = { error: (status + ' ' + res.statusText).trim() }
      if (status >= 400 || status === 0) {
        if (status === 401 && path !== '/api/login') this.expire()
        const err = new Error((data && data.error) || 'Request failed')
        err.status = status
        err.data = data || {}
        throw err
      }
      return data
    } finally {
      if (mutating) this.busyCount--
    }
  },

  get busy() { return this.busyCount > 0 },
  get showLogin() { return this.booted && !this.authed },
  get showApp() { return this.authed },

  tick() {
    this.now = Date.now()
  },

  syncRoute(first) {
    const parts = (location.hash || '').replace(/^#\/?/, '').split('/')
    const r = ROUTES.includes(parts[0]) ? parts[0] : 'dashboard'
    const sub = decodeURIComponent(parts[1] || '')
    if (r === this.route && sub === this.sub && !first) return
    const moved = r !== this.route
    this.route = r
    this.sub = sub
    this.hover = -1
    this.navOpen = false
    this.menu = ''
    if (r === 'peers') this.fStatus = sub
    if (!first) { if (moved) window.scrollTo(0, 0); this.refresh(true) }
    this.$nextTick(() => this.measureChart())
  },
  navCurrent(r) { return this.route === r ? 'page' : false },
  isRoute(r) { return this.route === r },
  navHref(it) { return '#/' + it.r },
  navBadge(it) {
    if (it.r === 'doctor') { const c = this.dCounts; return c.fail + c.warn ? String(c.fail + c.warn) : '' }
    if (it.r === 'egress') return this.hasChanges ? 'draft' : ''
    if (it.r === 'system') return this.overview.update ? 'update' : ''
    return ''
  },
  navBadgeClass(it) {
    if (it.r === 'doctor') return 'badge side-count ' + (this.dCounts.fail ? 'badge-fail' : 'badge-warn')
    if (it.r === 'egress') return 'badge side-count badge-warn'
    return 'badge side-count badge-brand'
  },
  ic(n) { return '#' + n },
  hasNavBadge(it) { return !!this.navBadge(it) },
  toggleNav() {
    this.navOpen = !this.navOpen
    if (this.navOpen) this.$nextTick(() => { const el = document.querySelector('.tnav [aria-current="page"]') || document.querySelector('.tnav .nav-link'); if (el) el.focus() })
  },
  closeNav() { if (this.navOpen) { this.navOpen = false; if (this.$refs.navBtn) this.$refs.navBtn.focus() } },
  navAway() { this.navOpen = false },
  get navOpenAttr() { return this.navOpen ? 'true' : 'false' },

  setOverview(v) {
    this.overview = v
  },
  refresh(silent, poll) {
    if (!this.authed) return Promise.resolve()
    if (refreshRun) {
      if (poll) return refreshRun
      refreshLoud = refreshLoud || !silent
      refreshNext = refreshNext || refreshRun.then(() => { const s = !refreshLoud; refreshNext = null; refreshLoud = false; return this.refresh(s) })
      return refreshNext
    }
    refreshRun = this.loadRoute(silent, poll).finally(() => { refreshRun = null })
    return refreshRun
  },
  async loadRoute(silent, poll) {
    try {
      const jobs = ROW_ROUTES.includes(this.route) ? [] : [this.api('GET', '/api/overview').then(v => this.setOverview(v))]
      const fn = this['load_' + this.route]
      if (fn && (!poll || this.pollRoute())) jobs.push(fn.call(this))
      await Promise.all(jobs)
      this.buildChart()
    } catch (e) {
      if (!silent && this.authed) this.toast('fail', e.message)
    }
  },

  pollRoute() {
    if (STATIC_ROUTES.includes(this.route)) return false
    return this.route !== 'egress' || ++this.pollN % 6 === 0
  },

  toast(kind, text, href, link) {
    const id = Date.now() + Math.random()
    this.toasts.push({ id, kind, text, href: href || '', link: link || '' })
    setTimeout(() => { this.toasts = this.toasts.filter(t => t.id !== id) }, href ? 8000 : 4500)
  },
  toastIcon(t) { return t.kind === 'ok' ? 'i-check-circle' : t.kind === 'warn' ? 'i-alert' : 'i-x-circle' },
  toastClass(t) { return 'toast toast-' + t.kind },
  dismissToast(t) { this.toasts = this.toasts.filter(x => x.id !== t.id) },
  async copy(text, what) {
    try { await navigator.clipboard.writeText(text) } catch (e) {
      const ta = document.createElement('textarea')
      ta.value = text
      document.body.appendChild(ta)
      ta.select()
      document.execCommand('copy')
      ta.remove()
    }
    this.toast('ok', (what || 'Text') + ' copied')
  },

  themeIs(v) { return this.themeMode === v },
  themeChecked(v) { return this.themeMode === v ? 'true' : 'false' },
  get themeLabel() { return 'Theme: ' + this.themeMode },
  setTheme(v) { this.themeMode = v; theme.set(v); this.closeMenu() },

  openMenu(id, e) {
    if (this.menu === id) { this.closeMenu(); return }
    this.menu = id
    this.menuBtn = e ? e.currentTarget : null
    this.$nextTick(() => {
      const m = document.querySelector('[data-menu="' + id + '"]')
      if (!m) return
      const el = m.querySelector('[aria-checked="true"]') || m.querySelector('[role^="menuitem"]')
      if (el) el.focus()
    })
  },
  menuShown(id) { return this.menu === id },
  menuExpanded(id) { return this.menu === id ? 'true' : 'false' },
  closeMenu() {
    if (!this.menu) return
    this.menu = ''
    if (this.menuBtn && document.contains(this.menuBtn)) this.menuBtn.focus()
  },
  clickAway(id) { if (this.menu === id) this.menu = '' },
  menuKey(e) {
    const items = Array.from(e.currentTarget.querySelectorAll('[role^="menuitem"]:not([disabled])'))
    const i = items.indexOf(document.activeElement)
    if (e.key === 'ArrowDown') { e.preventDefault(); items[(i + 1) % items.length].focus() }
    if (e.key === 'ArrowUp') { e.preventDefault(); items[(i - 1 + items.length) % items.length].focus() }
    if (e.key === 'Home') { e.preventDefault(); items[0].focus() }
    if (e.key === 'End') { e.preventDefault(); items[items.length - 1].focus() }
    if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); this.closeMenu() }
    if (e.key === 'Tab') this.menu = ''
  },

  async submitLogin() {
    this.login.error = ''
    if (!this.login.password) { this.login.error = 'Enter the password'; this.$refs.pw.focus(); return }
    if (this.totp && !/^\d{6}$/.test(this.login.code)) { this.login.error = 'Enter the 6-digit code from your authenticator app'; this.$refs.code.focus(); return }
    this.login.busy = true
    try {
      const r = await this.api('POST', '/api/login', { password: this.login.password, code: this.login.code })
      this.csrf = r.csrf
      this.authed = true
      this.loginNotice = ''
      this.login = { password: '', code: '', error: '', busy: false }
      this.doctor = { ran_at: '', checks: [] }
      await this.refresh()
      this.pollJob()
    } catch (e) {
      this.login.error = e.message
      this.login.password = ''
      this.login.busy = false
      this.$nextTick(() => this.$refs.pw.focus())
    }
  },
  get loginInvalid() { return this.login.error ? 'true' : 'false' },
  get loginBusy() { return this.login.busy },
  onCodeInput(e) { this.login.code = e.target.value.replace(/\D/g, '').slice(0, 6); e.target.value = this.login.code },
  async logout() {
    try { await this.api('POST', '/api/logout') } catch (e) {}
    this.leave('')
  },
  expire() { if (this.authed) this.leave('Your session has expired. Sign in again.') },
  leave(notice) {
    this.authed = false
    this.loginNotice = notice
    this.applyChecks = []
    this.jobOpen = false
    this.menu = ''
    for (const d of document.querySelectorAll('dialog[open]')) d.close()
    this.$nextTick(() => { if (this.$refs.pw) this.$refs.pw.focus() })
  },

  waitRestart(msg, units) {
    this.restarting = true
    this.restartMsg = msg
    this.restartUnits = units
    this.jobOpen = false
    for (const d of document.querySelectorAll('dialog[open]')) d.close()
    const before = this.overview.server.started_at
    const until = Date.now() + 120000
    let down = false
    const poll = async () => {
      try {
        const s = await this.api('GET', '/api/session')
        if (before ? s.started_at !== before : down) { location.reload(); return }
      } catch (e) { down = true }
      if (Date.now() > until) { this.restartFailed = true; return }
      setTimeout(poll, 2000)
    }
    setTimeout(poll, 2000)
  },
  get restartNote() { return this.restartFailed ? 'The server did not come back in 2 minutes. Check journalctl ' + this.restartUnits + ' on the server, then reload this page.' : 'Waiting for the server…' },

  get serverName() { return this.overview.server.hostname },
  get serverVersion() { return this.overview.server.version },
  get showUpdate() { return !!this.overview.update && this.updateDismissed !== this.overview.update },
  dismissUpdate() {
    this.updateDismissed = this.overview.update
    try { localStorage.setItem('awgx-update-dismissed', this.updateDismissed) } catch (e) {}
  },

  openDialog(ref) {
    this.focusBack[ref] = document.activeElement
    this.menu = ''
    const d = this.$refs[ref]
    if (!d.open) d.showModal()
    this.$nextTick(() => { const el = d.querySelector('[data-autofocus]') || d.querySelector('input, select, textarea, button'); if (el) el.focus() })
  },
  closeDialog(ref) { const d = this.$refs[ref]; if (d && d.open) d.close() },
  onDialogClose(e) {
    const ref = e.target.dataset.ref
    const back = this.focusBack[ref]
    if (ref === 'exportDlg') this.exp = null
    if (back && document.contains(back)) back.focus()
  },
  backdrop(e) { if (e.target === e.currentTarget) e.currentTarget.close() },
  closeSelf(e) { const d = e.currentTarget.closest('dialog'); if (d) d.close() },

  ask(o) {
    this.cf = Object.assign({ title: '', text: '', impact: '', typed: '', input: '', label: 'Confirm', danger: false, busy: false, error: '', run: null }, o)
    this.openDialog('cfDlg')
  },
  get cfTyped() { return !!this.cf.typed },
  get cfImpact() { return !!this.cf.impact },
  get cfBody() { return !!(this.cf.impact || this.cf.typed || this.cf.error) },
  get cfNotTyped() { return !this.cf.typed },
  get cfBlocked() { return this.cf.busy || (!!this.cf.typed && this.cf.input.trim() !== this.cf.typed) },
  get cfBtnClass() { return 'btn ' + (this.cf.danger ? 'btn-destructive' : 'btn-primary') },
  get cfRole() { return this.cf.danger ? 'alertdialog' : 'dialog' },
  async cfGo() {
    if (this.cfBlocked) return
    this.cf.busy = true
    this.cf.error = ''
    try {
      await this.cf.run.call(this)
      this.closeDialog('cfDlg')
    } catch (e) { this.cf.error = e.message } finally { this.cf.busy = false }
  },

  async startJob(promise, after) {
    const r = await promise
    if (after) this.jobHooks[r.id] = after
    this.jobSeen = 0
    this.jobOpen = true
    await this.pollJob(r.id)
  },
  async pollJob(expect) {
    clearTimeout(this.jobTimer)
    let j
    try { j = await this.api('GET', '/api/job') } catch (e) {
      if (e.status !== 401) this.jobTimer = setTimeout(() => this.pollJob(expect), 2000)
      return
    }
    const prev = this.job
    this.job = j
    if (j && !j.done) {
      this.jobTimer = setTimeout(() => this.pollJob(), 900)
      if (!prev && !expect) this.jobOpen = false
    }
    if (j && j.done && ((prev && prev.id === j.id && !prev.done) || expect === j.id)) this.jobFinished(j)
    this.$nextTick(() => { const el = this.$refs.jobLog; if (el) el.scrollTop = el.scrollHeight })
  },
  jobFinished(j) {
    const name = JOBS[j.kind] || j.kind
    if (j.error) { this.toast('fail', name + ' failed'); this.jobOpen = true } else { this.toast('ok', name + ': done'); this.jobOpen = false }
    const h = this.jobHooks[j.id]
    delete this.jobHooks[j.id]
    if (h) h.call(this, j)
    this.refresh(true)
  },
  get hasJob() { return !!this.job && this.jobSeen !== this.job.id },
  get jobShown() { return this.hasJob && this.jobOpen },
  get jobRunning() { return !!this.job && !this.job.done },
  get jobDone() { return !!this.job && this.job.done },
  get jobFailed() { return !!this.job && this.job.done && !!this.job.error },
  get jobOk() { return !!this.job && this.job.done && !this.job.error },
  get jobBanner() { return this.authed && this.jobRunning && !this.jobOpen },
  get jobTitle() { return this.job ? JOBS[this.job.kind] || this.job.kind : '' },
  get jobStep() {
    const j = this.job
    if (!j) return ''
    if (!j.total) return j.done ? (j.error ? 'Failed' : 'Finished') : 'Running'
    if (j.done) return j.error ? 'Failed at step ' + j.step + ' of ' + j.total : 'Finished · ' + plural(j.total, 'step', 'steps')
    return 'Step ' + j.step + ' of ' + j.total + ' · ' + j.stage
  },
  get jobPct() { const j = this.job; return j ? (j.done || !j.total ? (j.done ? 100 : 4) : Math.max(4, (j.step - 1) / j.total * 100 + 100 / j.total / 2)) : 0 },
  get jobMeterClass() { return 'task-meter' + (this.jobFailed ? ' is-fail' : this.jobOk ? ' is-ok' : '') },
  get jobLogText() { return this.job ? this.job.log.join('\n') : '' },
  get jobError() { return this.job ? this.job.error : '' },
  showJob() { this.jobOpen = true },
  hideJob() { this.jobOpen = false },
  dismissJob() {
    this.jobSeen = this.job ? this.job.id : 0
    this.jobOpen = false
    try { localStorage.setItem('awgx-job-seen', String(this.jobSeen)) } catch (e) {}
  },
  copyLog() { this.copy(this.jobLogText, 'Log') },

  get attention() {
    const a = []
    const o = this.overview
    if (this.jobFailed && this.hasJob) a.push({ id: 'job', tone: 'fail', icon: 'i-x-circle', t: 'Task failed: ' + this.jobTitle, d: this.jobError, href: '#/dashboard', job: true })
    if (this.doctorError) a.push({ id: 'derr', tone: 'warn', icon: 'i-x-circle', t: 'Health checks did not run', d: this.doctorError, href: '#/doctor' })
    const c = this.dCounts
    if (c.fail) a.push({ id: 'dfail', tone: 'fail', icon: 'i-x-circle', t: plural(c.fail, 'health check fails', 'health checks fail'), d: this.doctor.checks.filter(x => x.status === 'fail').map(x => x.id).join(', '), href: '#/doctor' })
    if (c.warn) a.push({ id: 'dwarn', tone: 'warn', icon: 'i-alert', t: plural(c.warn, 'health warning', 'health warnings'), d: this.fixable.length ? plural(this.fixable.length, 'has an automatic fix', 'have automatic fixes') : 'No automatic fix', href: '#/doctor' })
    const exp = this.peers.filter(p => p.status === 'expired')
    if (exp.length) a.push({ id: 'exp', tone: 'warn', icon: 'i-clock', t: plural(exp.length, 'peer has expired', 'peers have expired'), d: exp.map(p => p.name).join(', '), href: '#/peers/expired' })
    for (const p of this.egress.profiles || []) if (p.health === 'down' || p.health === 'degraded') a.push({ id: 'h' + p.name, tone: p.health === 'down' ? 'fail' : 'warn', icon: 'i-route', t: 'Egress profile ' + p.name + ' is ' + p.health, d: 'Peers on it use the fallback: ' + (p.fallback || 'drop'), href: '#/egress/profiles' })
    if (this.hasChanges) a.push({ id: 'draft', tone: 'info', icon: 'i-git', t: 'Egress draft is not applied', d: 'Changes are waiting for review', href: '#/egress/routes' })
    if (o.update) a.push({ id: 'upd', tone: 'info', icon: 'i-up', t: 'awgx ' + o.update + ' is available', d: 'You run ' + o.server.version, href: '#/system/general' })
    return a
  },
  get attentionEmpty() { return this.attention.length === 0 },
  attClass(a) { return 'att-item tone-' + a.tone },
  attGo(a) { if (a.job) this.showJob() },
  async load_dashboard() {
    if (!this.doctorLoading) {
      this.doctorLoading = true
      this.api('GET', '/api/doctor?refresh=1').then(v => { this.doctor = v; this.doctorError = '' }, e => { this.doctorError = e.message }).finally(() => { this.doctorLoading = false })
    }
    await Promise.all([
      this.load_peers(false),
      this.api('GET', '/api/stats?brief=1&since=' + this.dashPeriod).then(v => { this.dashSeries = v.series }),
      this.api('GET', '/api/egress?brief=1').then(v => { this.egress = v; this.egressLoaded = true })
    ])
  },
  get multi() { return this.overview.instances.length > 1 },
  get instanceNames() { return this.overview.instances.map(i => i.name) },
  get summary() { const o = this.overview; return { online: o.online, peers: o.peers, rx: fmtBytes(o.rx), tx: fmtBytes(o.tx) } },
  get sinceText() { return 'since the interfaces came up' },
  instHref(i) { return '#/instances/' + i.name },
  upDot(x) { return x.up ? 'dot dot-on' : 'dot dot-fail' },
  instEndpoint(i) { return i.endpoint + ':' + i.port },
  instOnline(i) { return i.online + ' / ' + i.peers },
  get recent() {
    this.now
    return this.peers.filter(p => p.last_handshake).sort((a, b) => Date.parse(b.last_handshake) - Date.parse(a.last_handshake)).slice(0, 6)
  },
  get recentEmpty() { return this.peersLoaded && this.recent.length === 0 },

  rel(ts) { this.now; return fmtRel(ts) },
  abs(ts) { return ts ? fmtAbs(Date.parse(ts)) : 'No handshake yet' },
  bytes(v) { return fmtBytes(v) },

  async load_stats() {
    await Promise.all([this.load_peers(), this.api('GET', this.statsUrl()).then(v => { this.stats = v })])
  },
  statsUrl() { return '/api/stats?since=' + this.statsPeriod + (this.statsPeer ? '&peer=' + enc(this.statsPeer) : '') },
  periodIs(p) { return this.statsPeriod === p.id ? 'true' : 'false' },
  dashPeriodIs(p) { return this.dashPeriod === p.id ? 'true' : 'false' },
  async setDashPeriod(p) {
    try {
      const v = await this.api('GET', '/api/stats?brief=1&since=' + p.id)
      this.dashPeriod = p.id
      this.hover = -1
      this.dashSeries = v.series
      this.buildChart()
    } catch (e) { this.toast('fail', e.message) }
  },
  async setStatsPeriod(p) { this.statsPeriod = p.id; this.hover = -1; await this.loadStats() },
  async loadStats() {
    try { this.stats = await this.api('GET', this.statsUrl()); this.buildChart() } catch (e) { this.toast('fail', e.message) }
  },
  pickPeer(r) { this.statsPeer = this.statsPeer === r.peer ? '' : r.peer; this.hover = -1; this.loadStats() },
  pickPeerKey(e, r) { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); this.pickPeer(r) } },
  clearPeer() { this.statsPeer = ''; this.loadStats() },
  rowSel(r) { return this.statsPeer === r.peer ? 'true' : 'false' },
  get statsRows() { return this.stats.peers.slice().sort((a, b) => (b.rx + b.tx) - (a.rx + a.tx)) },
  sumSeries(s) { return { rx: fmtBytes(s.reduce((a, p) => a + p.rx, 0)), tx: fmtBytes(s.reduce((a, p) => a + p.tx, 0)) } },
  get statsTotals() { return this.sumSeries(this.stats.series) },
  get dashTotals() { return this.sumSeries(this.dashSeries) },
  failClass(r) { return r.failures ? 'num fail-text' : 'num muted' },
  endpointText(r) { return r.endpoint || '—' },

  measureChart() {
    const c = document.getElementById('chart'), slot = document.getElementById(this.route === 'stats' ? 'slot-stats' : 'slot-dash')
    if (c && slot && c.parentNode !== slot) slot.appendChild(c)
    for (const el of document.querySelectorAll('[data-chart]')) if (el.clientWidth > 0) this.chartW = el.clientWidth
    this.buildChart()
  },
  observeCharts() {
    const ro = new ResizeObserver(entries => {
      for (const e of entries) {
        const w = Math.round(e.contentRect.width)
        if (w > 0 && w !== this.chartW) { this.chartW = w; this.buildChart() }
      }
    })
    for (const el of document.querySelectorAll('[data-chart]')) ro.observe(el)
  },
  get chartSeries() { return this.route === 'stats' ? this.stats.series : this.dashSeries },
  get chartPeriod() { return this.route === 'stats' ? this.statsPeriod : this.dashPeriod },
  buildChart() {
    const s = this.chartSeries
    const per = this.chartPeriod
    const W = Math.max(280, this.chartW), H = W < 520 ? 200 : 240, L = 64, R = 8, T = 10, B = 26
    const pw = W - L - R, ph = H - T - B, n = s.length
    const max = niceMax(Math.max(1024, ...s.map(p => Math.max(p.rx, p.tx))))
    const x = i => L + (n < 2 ? 0 : i * pw / (n - 1))
    const y = v => T + ph - v / max * ph
    const line = k => s.map((p, i) => (i ? 'L' : 'M') + x(i).toFixed(1) + ' ' + y(p[k]).toFixed(1)).join('')
    const area = k => n ? line(k) + 'L' + x(n - 1).toFixed(1) + ' ' + (T + ph) + 'L' + L + ' ' + (T + ph) + 'Z' : ''
    const yTicks = [0, 1, 2, 3, 4].map(j => ({ y: T + ph - j * ph / 4 + 4, label: fmtBytes(max * j / 4) }))
    const grid = [1, 2, 3, 4].map(j => 'M' + L + ' ' + (T + ph - j * ph / 4) + 'H' + (W - R)).join('')
    const count = W < 520 ? 3 : 6
    const long = per === '168h' || per === '720h'
    const xTicks = []
    for (let j = 0; j < 6; j++) {
      if (j >= count || n < 2) { xTicks.push({ x: 0, label: '', anchor: 'middle' }); continue }
      const i = Math.round(j * (n - 1) / (count - 1))
      const t = s[i].at * 1000
      xTicks.push({ x: x(i), label: long ? fmtDate(t) : fmtTime(t), anchor: j === 0 ? 'start' : j === count - 1 ? 'end' : 'middle' })
    }
    this.ch = {
      viewBox: '0 0 ' + W + ' ' + H, W, H, L, R, T, pw, ph, n, max, long,
      rxLine: line('rx'), txLine: line('tx'), rxArea: area('rx'), txArea: area('tx'),
      grid, base: 'M' + L + ' ' + (T + ph) + 'H' + (W - R), yTicks, xTicks, xLabelY: H - 6, yLabelX: L - 8, bottom: T + ph,
      empty: s.every(p => !p.rx && !p.tx)
    }
  },
  chartMove(e) {
    const c = this.ch
    if (c.n < 2) return
    const svg = e.currentTarget.ownerSVGElement || e.currentTarget
    const b = svg.getBoundingClientRect()
    const px = (e.clientX - b.left) * c.W / b.width
    this.hover = Math.max(0, Math.min(c.n - 1, Math.round((px - c.L) / c.pw * (c.n - 1))))
  },
  chartLeave() { this.hover = -1 },
  chartKey(e) {
    const n = this.ch.n
    if (!n) return
    if (e.key === 'ArrowRight') this.hover = this.hover < 0 ? n - 1 : Math.min(n - 1, this.hover + 1)
    else if (e.key === 'ArrowLeft') this.hover = this.hover < 0 ? n - 1 : Math.max(0, this.hover - 1)
    else if (e.key === 'Home') this.hover = 0
    else if (e.key === 'End') this.hover = n - 1
    else if (e.key === 'Escape') this.hover = -1
    else return
    e.preventDefault()
  },
  get tip() {
    const c = this.ch, s = this.chartSeries, p = s[this.hover]
    if (this.hover < 0 || !p || c.n < 2) return { show: false, x: 0, yRx: 0, yTx: 0, transform: '', time: '', rx: '', tx: '' }
    const x = c.L + this.hover * c.pw / (c.n - 1)
    const y = v => c.T + c.ph - v / c.max * c.ph
    const tx = x + 184 > c.W - c.R ? x - 184 : x + 12
    const t = p.at * 1000
    return { show: true, x, yRx: y(p.rx), yTx: y(p.tx), transform: 'translate(' + tx.toFixed(1) + ' ' + (c.T + 4) + ')', time: fmtDate(t) + ', ' + fmtTime(t), rx: fmtBytes(p.rx), tx: fmtBytes(p.tx) }
  },
  get chartLabel() {
    const t = this.route === 'stats' ? this.statsTotals : this.dashTotals
    return 'Traffic chart. Sent ' + t.tx + ', received ' + t.rx + '. Use arrow keys to inspect points.'
  },

  async load_journal() { this.journal = await this.api('GET', '/api/journal') },
  get jPrefixes() { return Array.from(new Set(this.journal.map(e => e.action.split('.')[0]))) },
  jPressed(p) { return this.jFilter === p ? 'true' : 'false' },
  get jAllPressed() { return this.jFilter ? 'false' : 'true' },
  setJFilter(p) { this.jFilter = this.jFilter === p ? '' : p },
  clearJFilter() { this.jFilter = '' },
  get journalDays() {
    const days = []
    const today = new Date(clock()).toDateString()
    const yest = new Date(clock() - 86400000).toDateString()
    for (const e of this.journal) {
      if (this.jFilter && e.action !== this.jFilter && !e.action.startsWith(this.jFilter + '.')) continue
      const t = Date.parse(e.at)
      const key = new Date(t).toDateString()
      let d = days[days.length - 1]
      if (!d || d.key !== key) {
        const label = key === today ? 'Today' : key === yest ? 'Yesterday' : ''
        d = { key, label: (label ? label + ' · ' : '') + fmtDate(t) + ', ' + new Date(t).getFullYear(), items: [] }
        days.push(d)
      }
      const lines = e.detail.split('\n')
      const diff = lines.slice(1).filter(l => /^[+-] /.test(l))
      const isDiff = diff.length > 0 && diff.length === lines.length - 1
      d.items.push({ id: e.at + e.action + d.items.length, time: fmtTime(t, true), action: e.action, source: e.source || '', title: isDiff ? lines[0] : e.detail, diff: isDiff ? diffLines(diff) : [], isDiff })
    }
    return days
  },
  get journalEmpty() { return this.journalDays.length === 0 },
  actionClass(e) { return 'badge badge-mono' + (e.action.endsWith('rollback') ? ' badge-warn' : e.action.startsWith('egress') ? ' badge-brand' : '') }
}))
