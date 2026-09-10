import { useEffect, useState } from 'react'
import { api } from '../api'
import { styles } from '../styles'
import { Icon } from '../icons'
import { fmtNum } from '../format'
import type { DashboardSummary, StoreForwardStatus, SystemInfo, TimeStatus } from '../types'

function fmtPercent(v?: number) {
  return v === undefined ? '—' : `${v.toFixed(1)}%`
}

function fmtBytes(v?: number) {
  if (v === undefined) return '—'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let n = v
  let i = 0
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024
    i++
  }
  return `${n.toFixed(1)} ${units[i]}`
}

// timeUntilEvictionSeconds estimates, at the current write rate, how long
// until eviction is forced to start deleting not-yet-sent (PENDING/
// SENDING) rows rather than already-SENT ones. Acquisition always writes
// to data_queue regardless of server connectivity (Rule 1 - see
// HANDOFF.md), so this holds whether the server is currently reachable or
// not: it's the answer to "if we lost the connection right now, how long
// before undelivered data is actually at risk."
//
// Deliberately NOT (max_rows - total_rows) / write_rate - that measures
// time until the *next eviction tick*, not time until real data loss.
// Once RunMaxRowsSweeper is keeping total_rows hovering near max_rows (its
// intended steady state), that number is small essentially all the time
// even though the server is connected and every evicted row is already-
// SENT and safe to lose - a false alarm found live on 2026-09-10.
//
// The correct budget doesn't depend on total_rows at all: SENT rows are
// evicted before PENDING/SENDING ones (oldest-non-critical-first already
// prefers whatever isn't still needed), so the number of *new* rows that
// can arrive before eviction is forced into PENDING/SENDING data is
// max_rows - currently-undelivered-rows, regardless of how much SENT
// buffer currently exists - that buffer is fully counted, implicitly, by
// not subtracting total_rows.
//
// Caveat: this assumes SENT rows are evicted before PENDING/SENDING ones
// in practice. Eviction order is actually priority-tier first (LOW before
// NORMAL before HIGH) and oldest-within-tier second, regardless of
// status - so if PENDING data is concentrated in a lower priority tier
// than old SENT data, eviction could reach a PENDING row earlier than
// this estimate implies. Still far more accurate than measuring against
// total_rows.
function timeUntilEvictionSeconds(
  maxRows?: number,
  pendingRecords?: number,
  sendingRecords?: number,
  writeRatePerSec?: number,
): number | null {
  if (maxRows == null || pendingRecords == null || sendingRecords == null || writeRatePerSec == null || writeRatePerSec <= 0)
    return null
  const rowsRemaining = maxRows - pendingRecords - sendingRecords
  if (rowsRemaining <= 0) return 0
  return rowsRemaining / writeRatePerSec
}

function fmtDuration(seconds: number | null): string {
  if (seconds === null) return '—'
  if (seconds <= 0) return 'now'
  if (seconds < 60) return `${seconds.toFixed(0)}s`
  const minutes = seconds / 60
  if (minutes < 60) return `${minutes.toFixed(0)}m`
  const hours = minutes / 60
  if (hours < 48) return `${hours.toFixed(1)}h`
  return `${(hours / 24).toFixed(1)}d`
}

function timeQualityBadgeStyle(quality?: string) {
  if (quality === 'SYNCED') return styles.badgeGood
  if (quality === 'RTC' || quality === 'UNSYNCED') return styles.badgeNeutral
  return styles.badgeBad
}

export function Dashboard() {
  const [system, setSystem] = useState<SystemInfo | null>(null)
  const [summary, setSummary] = useState<DashboardSummary | null>(null)
  const [storeForward, setStoreForward] = useState<StoreForwardStatus | null>(null)
  const [time, setTime] = useState<TimeStatus | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    const load = () => {
      Promise.all([api.getSystem(), api.getDashboardSummary(), api.getStoreForwardStatus(), api.getTime()])
        .then(([sys, sum, sf, t]) => {
          setSystem(sys)
          setSummary(sum)
          setStoreForward(sf)
          setTime(t)
          setError(null)
        })
        .catch((err) => setError(String(err instanceof Error ? err.message : err)))
    }

    load()
    const interval = setInterval(load, 5000)
    return () => clearInterval(interval)
  }, [])

  if (error && !system) {
    return (
      <div>
        <h2 style={{ marginTop: 0 }}>Dashboard</h2>
        <div style={styles.errorBox}>Cannot reach gateway API: {error}</div>
      </div>
    )
  }

  if (!system || !summary || !storeForward || !time) {
    return (
      <div>
        <h2 style={{ marginTop: 0 }}>Dashboard</h2>
        <p style={styles.muted}>Loading…</p>
      </div>
    )
  }

  return (
    <div>
      <h2 style={{ marginTop: 0 }}>Dashboard</h2>
      {error && <div style={styles.errorBox}>Last refresh failed: {error}</div>}

      <div style={styles.cardGrid}>
        <div style={styles.card}>
          <div style={styles.cardIcon}><Icon name="gateway" /></div>
          <div style={styles.cardTitle}>Gateway Status</div>
          <div style={styles.cardValue}>{system.status === 'ok' ? 'Running' : system.status}</div>
          <div style={styles.cardSub}>uptime {(system.uptime_seconds / 60).toFixed(1)} min</div>
        </div>

        <div style={styles.card}>
          <div style={styles.cardIcon}><Icon name="cpu" /></div>
          <div style={styles.cardTitle}>CPU</div>
          <div style={styles.cardValue}>{fmtPercent(system.cpu_percent)}</div>
          <div style={styles.progressTrack}>
            <div style={styles.progressFill(system.cpu_percent ?? 0, (system.cpu_percent ?? 0) >= 90)} />
          </div>
        </div>

        <div style={styles.card}>
          <div style={styles.cardIcon}><Icon name="ram" /></div>
          <div style={styles.cardTitle}>RAM</div>
          <div style={styles.cardValue}>{fmtPercent(system.mem_used_percent)}</div>
          <div style={styles.progressTrack}>
            <div style={styles.progressFill(system.mem_used_percent ?? 0, (system.mem_used_percent ?? 0) >= 90)} />
          </div>
          <div style={styles.cardSub}>
            {system.mem_used_mb?.toFixed(0)} / {system.mem_total_mb?.toFixed(0)} MB
          </div>
        </div>

        <div style={styles.card}>
          <div style={styles.cardIcon}><Icon name="storage" /></div>
          <div style={styles.cardTitle}>Storage</div>
          <div style={styles.cardValue}>{fmtPercent(system.disk_used_percent)}</div>
          <div style={styles.progressTrack}>
            <div style={styles.progressFill(system.disk_used_percent ?? 0, (system.disk_used_percent ?? 0) >= 90)} />
          </div>
          <div style={styles.cardSub}>
            {system.disk_used_gb?.toFixed(1)} / {system.disk_total_gb?.toFixed(1)} GB
          </div>
        </div>

        <div style={styles.card}>
          <div style={styles.cardIcon}><Icon name="storage" /></div>
          <div style={styles.cardTitle}>Database Size</div>
          <div style={styles.cardValue}>{fmtBytes(system.database_size_bytes)}</div>
          <div style={styles.cardSub}>gateway.db, incl. WAL</div>
        </div>

        <div style={styles.card}>
          <div style={styles.cardIcon}><Icon name="network" /></div>
          <div style={styles.cardTitle}>Network (cumulative)</div>
          <div style={styles.cardValue}>{fmtBytes(system.net_bytes_sent)}</div>
          <div style={styles.cardSub}>sent · {fmtBytes(system.net_bytes_recv)} received</div>
        </div>

        <div style={styles.card}>
          <div style={styles.cardIcon}><Icon name="devices" /></div>
          <div style={styles.cardTitle}>Devices</div>
          <div style={styles.cardValue}>
            {summary.enabled_device_count} / {summary.device_count}
          </div>
          <div style={styles.cardSub}>enabled / total</div>
        </div>

        <div style={styles.card}>
          <div style={styles.cardIcon}><Icon name="data-points" /></div>
          <div style={styles.cardTitle}>Data Points</div>
          <div style={styles.cardValue}>{fmtNum(summary.data_point_count)}</div>
        </div>

        <div style={styles.card}>
          <div style={styles.cardIcon}><Icon name="cloud" /></div>
          <div style={styles.cardTitle}>Server Connection</div>
          <div style={styles.cardValue}>
            <span style={storeForward.server_connected ? styles.badgeGood : styles.badgeBad}>
              <span style={styles.badgeDot} />
              {storeForward.server_connected ? 'Connected' : 'Disconnected'}
            </span>
          </div>
          {storeForward.server_last_error && <div style={styles.cardSub}>{storeForward.server_last_error}</div>}
        </div>

        <div style={styles.card}>
          <div style={styles.cardIcon}><Icon name="queue" /></div>
          <div style={styles.cardTitle}>Pending Queue</div>
          <div style={styles.cardValue}>{fmtNum(storeForward.pending_records)}</div>
          <div style={styles.cardSub}>{fmtNum(storeForward.retry_count)} retries so far</div>
        </div>

        <div style={styles.card}>
          <div style={styles.cardIcon}><Icon name="clock" /></div>
          <div style={styles.cardTitle}>Queue Size</div>
          <div style={styles.cardValue}>
            {storeForward.total_rows != null ? fmtNum(storeForward.total_rows) : '—'}
            {storeForward.max_rows != null ? ` / ${fmtNum(storeForward.max_rows)}` : ''}
          </div>
          <div style={styles.cardSub}>oldest non-critical records are evicted once max rows is exceeded</div>
        </div>

        <div style={styles.card}>
          <div style={styles.cardIcon}><Icon name="activity" /></div>
          <div style={styles.cardTitle}>Queue Write Rate</div>
          <div style={styles.cardValue}>
            {storeForward.write_rate_per_sec != null ? `${storeForward.write_rate_per_sec.toFixed(1)} rows/s` : '—'}
          </div>
          <div style={styles.cardSub}>must stay below eviction capacity for Queue Size to stay bounded</div>
        </div>

        <div style={styles.card}>
          <div style={styles.cardIcon}><Icon name="timeout" /></div>
          <div style={styles.cardTitle}>Est. Time Until Data Loss Risk</div>
          <div style={styles.cardValue}>
            {fmtDuration(
              timeUntilEvictionSeconds(
                storeForward.max_rows,
                storeForward.pending_records,
                storeForward.sending_records,
                storeForward.write_rate_per_sec,
              ),
            )}
          </div>
          <div style={styles.cardSub}>
            if the server stays unreachable starting now, how long until not-yet-sent data is actually at risk of
            being overwritten (not just when Queue Size next evicts already-sent records)
          </div>
        </div>

        <div style={styles.card}>
          <div style={styles.cardIcon}><Icon name="clock" /></div>
          <div style={styles.cardTitle}>Time Synchronization</div>
          <div style={styles.cardValue}>
            <span style={timeQualityBadgeStyle(time.time_quality)}>
              <span style={styles.badgeDot} />
              {time.time_quality}
            </span>
          </div>
          <div style={styles.cardSub}>{time.ntp_server || 'no NTP server configured'}</div>
        </div>
      </div>
    </div>
  )
}
