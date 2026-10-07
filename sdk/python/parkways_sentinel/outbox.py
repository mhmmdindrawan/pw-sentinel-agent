"""Where the SDK keeps events until the agent has acknowledged them.

Two implementations with the same interface, both used only from the
client's worker thread:

- MemoryOutbox: lost when the app exits; the default.
- SqliteOutbox: survives app restarts and agent/network outages. Writes
  are batched by the caller (one transaction per batch) to spare the SD
  cards in ARM kiosks.

When full, both discard the oldest normal-priority events first and only
then the oldest high-priority ones (see events.HIGH_PRIORITY).

Python 3.6-compatible, stdlib only.
"""

import collections
import json


class MemoryOutbox(object):
    def __init__(self, max_rows):
        self._max_rows = max_rows
        self._rows = collections.OrderedDict()  # id -> (created, priority, payload)
        self._next_id = 1

    def add_many(self, items, now):
        """Store (payload, priority) pairs; return how many rows were
        discarded to stay within max_rows."""
        for payload, priority in items:
            self._rows[self._next_id] = (now, priority, payload)
            self._next_id += 1
        return self._enforce_max()

    def peek(self, n):
        out = []
        for row_id, (_, _, payload) in self._rows.items():
            if len(out) >= n:
                break
            out.append((row_id, payload))
        return out

    def delete(self, ids):
        for row_id in ids:
            self._rows.pop(row_id, None)

    def count(self):
        return len(self._rows)

    def expire(self, cutoff):
        old = [i for i, (created, _, _) in self._rows.items() if created < cutoff]
        self.delete(old)
        return len(old)

    def close(self):
        pass

    def _enforce_max(self):
        excess = len(self._rows) - self._max_rows
        if excess <= 0:
            return 0
        # Oldest normal-priority first, then oldest high-priority.
        victims = sorted(self._rows, key=lambda i: (self._rows[i][1], i))[:excess]
        self.delete(victims)
        return len(victims)


class SqliteOutbox(object):
    def __init__(self, path, max_rows):
        import sqlite3  # some minimal Python builds lack it; caller falls back to memory

        self._max_rows = max_rows
        self._db = sqlite3.connect(path, timeout=5, isolation_level=None)
        # WAL + synchronous=NORMAL: one fsync per checkpoint instead of per
        # commit. A power cut can lose the last few unsent events, never
        # corrupt the file.
        self._db.execute("PRAGMA journal_mode=WAL")
        self._db.execute("PRAGMA synchronous=NORMAL")
        self._db.execute(
            "CREATE TABLE IF NOT EXISTS outbox ("
            " id INTEGER PRIMARY KEY AUTOINCREMENT,"
            " created REAL NOT NULL,"
            " priority INTEGER NOT NULL,"
            " payload TEXT NOT NULL)"
        )

    def add_many(self, items, now):
        if not items:
            return 0
        self._db.execute("BEGIN")
        try:
            self._db.executemany(
                "INSERT INTO outbox (created, priority, payload) VALUES (?, ?, ?)",
                [(now, priority, json.dumps(payload)) for payload, priority in items],
            )
            dropped = self._enforce_max()
            self._db.execute("COMMIT")
        except Exception:
            self._db.execute("ROLLBACK")
            raise
        return dropped

    def peek(self, n):
        rows = self._db.execute("SELECT id, payload FROM outbox ORDER BY id LIMIT ?", (n,)).fetchall()
        return [(row_id, json.loads(payload)) for row_id, payload in rows]

    def delete(self, ids):
        if not ids:
            return
        marks = ",".join("?" * len(ids))
        self._db.execute("DELETE FROM outbox WHERE id IN (%s)" % marks, list(ids))

    def count(self):
        return self._db.execute("SELECT COUNT(*) FROM outbox").fetchone()[0]

    def expire(self, cutoff):
        return self._db.execute("DELETE FROM outbox WHERE created < ?", (cutoff,)).rowcount

    def close(self):
        self._db.close()

    def _enforce_max(self):
        excess = self.count() - self._max_rows
        if excess <= 0:
            return 0
        return self._db.execute(
            "DELETE FROM outbox WHERE id IN ("
            " SELECT id FROM outbox ORDER BY priority ASC, id ASC LIMIT ?)",
            (excess,),
        ).rowcount
