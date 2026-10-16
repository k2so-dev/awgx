package state

var migrations = []string{
	`CREATE TABLE instances (
		name TEXT PRIMARY KEY,
		listen_port INTEGER NOT NULL,
		endpoint TEXT NOT NULL,
		mtu INTEGER NOT NULL,
		subnet4 TEXT NOT NULL,
		subnet6 TEXT NOT NULL,
		dns TEXT NOT NULL,
		preset TEXT NOT NULL,
		obfuscation TEXT NOT NULL,
		i1 TEXT NOT NULL,
		i1_rotated_at TEXT NOT NULL,
		private_key BLOB NOT NULL,
		header_protection_key BLOB NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);
	CREATE TABLE peers (
		id INTEGER PRIMARY KEY,
		instance TEXT NOT NULL REFERENCES instances(name) ON DELETE CASCADE,
		name TEXT NOT NULL UNIQUE,
		public_key TEXT NOT NULL UNIQUE,
		private_key BLOB NOT NULL,
		preshared_key BLOB,
		address4 TEXT NOT NULL,
		address6 TEXT NOT NULL,
		allowed_ips TEXT NOT NULL,
		egress TEXT NOT NULL,
		keepalive TEXT NOT NULL,
		i1 TEXT NOT NULL,
		i1_cps TEXT NOT NULL,
		net_label TEXT NOT NULL,
		enabled INTEGER NOT NULL,
		expires_at TEXT NOT NULL,
		note TEXT NOT NULL,
		created_at TEXT NOT NULL
	);
	CREATE TABLE artifacts (
		path TEXT PRIMARY KEY,
		kind TEXT NOT NULL,
		created_at TEXT NOT NULL
	);
	CREATE TABLE journal (
		id INTEGER PRIMARY KEY,
		at TEXT NOT NULL,
		action TEXT NOT NULL,
		detail TEXT NOT NULL
	);
	CREATE TABLE settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);`,
	`CREATE TABLE traffic (
		peer_id INTEGER NOT NULL REFERENCES peers(id) ON DELETE CASCADE,
		bucket INTEGER NOT NULL,
		ts INTEGER NOT NULL,
		rx INTEGER NOT NULL,
		tx INTEGER NOT NULL,
		handshakes INTEGER NOT NULL,
		failures INTEGER NOT NULL,
		last_handshake INTEGER NOT NULL,
		endpoint TEXT NOT NULL,
		PRIMARY KEY (peer_id, bucket, ts)
	);
	CREATE TABLE sessions (
		id INTEGER PRIMARY KEY,
		peer_id INTEGER NOT NULL REFERENCES peers(id) ON DELETE CASCADE,
		i1_profile TEXT NOT NULL,
		net_label TEXT NOT NULL,
		started_at INTEGER NOT NULL,
		ended_at INTEGER NOT NULL,
		open INTEGER NOT NULL
	);
	CREATE INDEX sessions_peer ON sessions(peer_id, open);`,
	`CREATE TABLE egress_profiles (
		name TEXT PRIMARY KEY,
		kind TEXT NOT NULL,
		config BLOB NOT NULL,
		created_at TEXT NOT NULL
	);
	CREATE TABLE egress_snapshots (
		id INTEGER PRIMARY KEY,
		created_at TEXT NOT NULL,
		status TEXT NOT NULL,
		config BLOB NOT NULL,
		base INTEGER NOT NULL,
		deadline TEXT NOT NULL,
		admin TEXT NOT NULL,
		diff TEXT NOT NULL,
		checks TEXT NOT NULL
	);
	CREATE TABLE uplinks (
		name TEXT PRIMARY KEY,
		config BLOB NOT NULL,
		routed INTEGER NOT NULL,
		created_at TEXT NOT NULL
	);
	ALTER TABLE instances ADD COLUMN egress TEXT NOT NULL DEFAULT 'direct';
	ALTER TABLE instances ADD COLUMN killswitch INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE peers ADD COLUMN killswitch INTEGER NOT NULL DEFAULT 0;
	UPDATE peers SET egress = '' WHERE egress = 'direct';`,
	`ALTER TABLE journal ADD COLUMN source TEXT NOT NULL DEFAULT '';`,
}
