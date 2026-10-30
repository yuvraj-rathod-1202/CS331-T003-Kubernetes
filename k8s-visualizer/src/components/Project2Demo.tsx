import { useCallback, useEffect, useRef, useState } from 'react';
import {
  AlertTriangle,
  CheckCircle2,
  Flame,
  HeartHandshake,
  Layers,
  RotateCcw,
  Server,
  ShieldAlert,
  Shuffle,
  Terminal as TerminalIcon,
  XCircle,
} from 'lucide-react';
import './Project2Demo.css';

// Everything on this page comes from the API below. Nothing is simulated: if a
// value cannot be read from the cluster, the page says so instead of guessing.
const POLL_MS = 2000;
const OPERATOR_STALE_SECONDS = 30;
const MAX_EVENTS = 60;

interface ClusterState {
  success: boolean;
  error?: string;
  nodes?: any[];
  faultRules?: Record<string, number | null>;
  cr?: any | null;
  pods?: any[];
  calicoPods?: any[];
  endpointSlices?: any[];
  nadConfig?: string | null;
}

interface TrafficSample {
  epoch: number;
  httpCode: string;
  seconds: number;
}

interface TrafficState {
  deployed: boolean;
  samples: TrafficSample[];
  error?: string;
}

interface EventLine {
  time: string;
  text: string;
  kind: 'change' | 'action' | 'error';
}

function isConditionTrue(obj: any, type: string): boolean {
  return (obj?.status?.conditions ?? []).some((c: any) => c.type === type && c.status === 'True');
}

function net1IP(pod: any): string | null {
  try {
    const entries = JSON.parse(pod.metadata?.annotations?.['k8s.v1.cni.cncf.io/network-status'] ?? '[]');
    return entries.find((e: any) => e.interface === 'net1')?.ips?.[0] ?? null;
  } catch {
    return null;
  }
}

function backupNetworkType(nadConfig: string | null | undefined): string | null {
  if (!nadConfig) return null;
  try {
    return JSON.parse(nadConfig).type ?? 'unknown';
  } catch {
    return 'unreadable';
  }
}

function operatorState(cr: any, now: number): { label: string; running: boolean } {
  if (!cr) return { label: 'No CNIResilience CR', running: false };
  const last = cr.status?.lastReconcileTime;
  if (!last) return { label: 'Never reconciled', running: false };
  const age = Math.round((now - new Date(last).getTime()) / 1000);
  if (age > OPERATOR_STALE_SECONDS) return { label: `Not running (last seen ${age}s ago)`, running: false };
  return { label: 'Running', running: true };
}

// Flattens the cluster into "name → value" pairs. Comparing two snapshots gives
// the event log, so every line in it is a change that was really observed.
function snapshot(state: ClusterState): Map<string, string> {
  const out = new Map<string, string>();
  const status = state.cr?.status;
  if (status) {
    out.set('Operator phase', status.phase ?? 'none');
    out.set('Failover count', String(status.failoverCount ?? 0));
    out.set('Recovery count', String(status.recoveryCount ?? 0));
    for (const n of status.nodeStatuses ?? []) {
      out.set(`${n.nodeName}: operator active path`, n.activePath);
      out.set(`${n.nodeName}: operator says primary`, n.primaryHealthy ? 'healthy' : 'down');
      out.set(`${n.nodeName}: operator says backup`, n.backupHealthy ? 'healthy' : 'down');
    }
  }
  for (const node of state.nodes ?? []) {
    const name = node.metadata.name;
    const taints = (node.spec.taints ?? []).map((t: any) => t.key).join(', ') || 'none';
    out.set(`${name}: node`, isConditionTrue(node, 'Ready') ? 'Ready' : 'NotReady');
    out.set(`${name}: taints`, taints);
    out.set(`${name}: demo fault rules`, String(state.faultRules?.[name] ?? 'unknown'));
  }
  for (const pod of state.calicoPods ?? []) {
    out.set(`${pod.spec.nodeName}: calico-node pod`, `${pod.metadata.name} ${isConditionTrue(pod, 'Ready') ? 'Ready' : 'NotReady'}`);
  }
  for (const slice of state.endpointSlices ?? []) {
    for (const ep of slice.endpoints ?? []) {
      out.set(`EndpointSlice ${slice.metadata.name} ${ep.addresses[0]}`, ep.conditions?.ready === false ? 'not ready' : 'ready');
    }
  }
  return out;
}

async function postJson(url: string, body: unknown) {
  const res = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  return res.json();
}

export function Project2Demo() {
  const [cluster, setCluster] = useState<ClusterState | null>(null);
  const [traffic, setTraffic] = useState<TrafficState | null>(null);
  const [now, setNow] = useState(() => Date.now());
  const [selectedNode, setSelectedNode] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [tab, setTab] = useState<'slices' | 'events'>('slices');
  const [events, setEvents] = useState<EventLine[]>([]);
  const previous = useRef<Map<string, string> | null>(null);

  const addEvent = useCallback((text: string, kind: EventLine['kind']) => {
    const time = new Date().toLocaleTimeString('en-GB');
    setEvents(prev => [...prev.slice(-(MAX_EVENTS - 1)), { time, text, kind }]);
  }, []);

  const refresh = useCallback(async () => {
    const [statusRes, trafficRes] = await Promise.allSettled([
      fetch('/api/fault/cniresilience/status').then(r => r.json()),
      fetch('/api/fault/cniresilience/traffic').then(r => r.json()),
    ]);

    const nextCluster: ClusterState = statusRes.status === 'fulfilled'
      ? statusRes.value
      : { success: false, error: 'The dev server did not answer' };
    setCluster(nextCluster);
    setNow(Date.now());
    setTraffic(trafficRes.status === 'fulfilled'
      ? trafficRes.value
      : { deployed: false, samples: [], error: 'The dev server did not answer' });

    if (nextCluster.success) {
      const current = snapshot(nextCluster);
      if (previous.current) {
        for (const [key, value] of current) {
          const before = previous.current.get(key);
          if (before !== value) addEvent(`${key}: ${before ?? 'new'} → ${value}`, 'change');
        }
      }
      previous.current = current;
    }
  }, [addEvent]);

  // A new poll starts only after the last one ended, so slow answers never overlap.
  useEffect(() => {
    let stopped = false;
    let timer: ReturnType<typeof setTimeout>;
    const loop = async () => {
      await refresh();
      if (!stopped) timer = setTimeout(loop, POLL_MS);
    };
    loop();
    return () => {
      stopped = true;
      clearTimeout(timer);
    };
  }, [refresh]);

  const nodes = cluster?.nodes ?? [];
  const workerNode = nodes.find(n => !('node-role.kubernetes.io/control-plane' in (n.metadata.labels ?? {})));
  const targetNode = selectedNode ?? workerNode?.metadata.name ?? nodes[0]?.metadata.name ?? null;

  const runAction = async (name: string, url: string, body: unknown) => {
    setBusy(name);
    addEvent(`You clicked: ${name}`, 'action');
    try {
      const result = await postJson(url, body);
      if (result.command) addEvent(`Ran: ${result.command}`, 'action');
      if (result.success) {
        addEvent(`OK: ${result.message ?? name}`, 'action');
      } else {
        addEvent(`FAILED: ${result.error ?? 'unknown error'}`, 'error');
      }
    } catch {
      addEvent(`FAILED: the dev server did not answer ${url}`, 'error');
    }
    setTab('events');
    setBusy(null);
    refresh();
  };

  // No cluster: show why, and nothing else.
  if (cluster && !cluster.success) {
    return (
      <div className="p2-page p2-centered">
        <div className="p2-notice p2-notice-error">
          <XCircle size={20} />
          <div>
            <strong>Cannot read the cluster</strong>
            <p>{cluster.error}</p>
            <code>minikube start -p k8s-experiments</code>
          </div>
        </div>
      </div>
    );
  }
  if (!cluster) {
    return <div className="p2-page p2-centered"><p className="p2-muted">Reading the cluster…</p></div>;
  }

  const cr = cluster.cr;
  const operator = operatorState(cr, now);
  const backupType = backupNetworkType(cluster.nadConfig);
  const targetFaultRules = targetNode ? cluster.faultRules?.[targetNode] ?? 0 : 0;

  const samples = traffic?.samples ?? [];
  const okSamples = samples.filter(s => s.httpCode === '200');
  const availability = samples.length ? Math.round((okSamples.length / samples.length) * 100) : null;
  const sortedMs = okSamples.map(s => s.seconds * 1000).sort((a, b) => a - b);
  const medianMs = sortedMs.length ? sortedMs[Math.floor(sortedMs.length / 2)] : null;
  const lastSample = samples[samples.length - 1];
  const failuresNow = lastSample ? lastSample.httpCode !== '200' : false;

  const notices: string[] = [];
  if (!cr) notices.push('No CNIResilience CR, so the operator columns are empty and its toggles are off. Apply operator/config/samples/cniresilience_sample.yaml.');
  else if (!operator.running) notices.push('The operator is not running: everything under "Operator says" is old data. Start it with make run.');
  if (!backupType) notices.push('Backup network is not installed: no Multus NetworkAttachmentDefinition "flannel-backup" was found.');
  else if (backupType !== 'flannel') notices.push(`Backup network is type "${backupType}", not Flannel.`);
  if (traffic && !traffic.deployed) notices.push('No real traffic data: the traffic-client pod is missing. Apply experiments/manifests/resilience-workload.yaml.');

  return (
    <div className="p2-page">
      <header className="p2-header">
        <div>
          <h1>Dual-CNI Resilience</h1>
          <p className="p2-muted">Live cluster state · refreshes every {POLL_MS / 1000}s</p>
        </div>
        <div className="p2-pills">
          <Pill label="Operator" value={operator.label} tone={operator.running ? 'good' : 'bad'} />
          <Pill label="Phase" value={cr?.status?.phase ?? 'none'} tone={cr?.status?.phase === 'Healthy' ? 'good' : cr?.status?.phase ? 'warn' : 'none'} />
          <Pill label="Failovers" value={String(cr?.status?.failoverCount ?? 0)} tone="none" />
          <Pill label="Recovery attempts" value={String(cr?.status?.recoveryCount ?? 0)} tone="none" />
        </div>
      </header>

      {notices.length > 0 && (
        <div className="p2-notice">
          <AlertTriangle size={18} />
          <ul>{notices.map(n => <li key={n}>{n}</li>)}</ul>
        </div>
      )}

      <main className="p2-grid">
        <section className="p2-panel p2-q-traffic">
          <h2>Service traffic <span className="p2-muted">measured by the traffic-client pod</span></h2>
          <div className="p2-stats">
            <Stat label="Now" value={lastSample ? (failuresNow ? 'FAILING' : 'OK') : '—'} tone={lastSample ? (failuresNow ? 'bad' : 'good') : 'none'} />
            <Stat label={`Availability (last ${samples.length})`} value={availability === null ? '—' : `${availability}%`} tone="none" />
            <Stat label="Median response" value={medianMs === null ? '—' : `${medianMs.toFixed(1)} ms`} tone="none" />
            <Stat label="Failed requests" value={String(samples.length - okSamples.length)} tone={samples.length !== okSamples.length ? 'bad' : 'none'} />
          </div>
          <div className="p2-bars" role="img" aria-label="One bar per real request. Red bars failed.">
            {samples.map(s => {
              const ok = s.httpCode === '200';
              const height = ok ? Math.min(100, 12 + Math.log10(1 + s.seconds * 1000) * 30) : 100;
              return (
                <div
                  key={s.epoch}
                  className={`p2-bar ${ok ? 'ok' : 'fail'}`}
                  style={{ height: `${height}%` }}
                  title={`${new Date(s.epoch * 1000).toLocaleTimeString('en-GB')}  HTTP ${s.httpCode}  ${(s.seconds * 1000).toFixed(1)} ms`}
                />
              );
            })}
            {samples.length === 0 && <p className="p2-muted">No samples yet</p>}
          </div>
        </section>

        <section className="p2-panel p2-q-nodes">
          <h2>Nodes <span className="p2-muted">click one to choose the target</span></h2>
          <div className="p2-nodes">
            {nodes.map(node => {
              const name = node.metadata.name;
              const ip = node.status.addresses?.find((a: any) => a.type === 'InternalIP')?.address;
              const calico = cluster.calicoPods?.find(p => p.spec.nodeName === name);
              const opStatus = cr?.status?.nodeStatuses?.find((s: any) => s.nodeName === name);
              const faults = cluster.faultRules?.[name];
              const taints = node.spec.taints ?? [];
              const nodePods = (cluster.pods ?? []).filter(p => p.spec.nodeName === name);
              return (
                <button
                  key={name}
                  className={`p2-node ${name === targetNode ? 'selected' : ''} ${faults ? 'faulted' : ''}`}
                  onClick={() => setSelectedNode(name)}
                >
                  <div className="p2-node-head">
                    <span className="p2-node-name">
                      <Server size={15} />
                      {name}
                    </span>
                    {name === targetNode && <span className="p2-tag target">TARGET</span>}
                  </div>
                  <p className="p2-mono p2-muted">{ip}</p>

                  <dl className="p2-facts">
                    <Fact label="Kubernetes node" value={isConditionTrue(node, 'Ready') ? 'Ready' : 'NotReady'} good={isConditionTrue(node, 'Ready')} />
                    <Fact label="calico-node pod" value={calico ? (isConditionTrue(calico, 'Ready') ? 'Ready' : 'NotReady') : 'Missing'} good={!!calico && isConditionTrue(calico, 'Ready')} />
                    <Fact label="Injected fault" value={faults === null || faults === undefined ? 'Unknown' : faults > 0 ? `Active (${faults} rule${faults > 1 ? 's' : ''})` : 'None'} good={faults === 0} />
                    <Fact label="Taints" value={taints.length ? taints.map((t: any) => t.key.split('/').pop()).join(', ') : 'None'} good={taints.length === 0} />
                  </dl>

                  <h3>Operator says</h3>
                  {opStatus ? (
                    <>
                      <dl className="p2-facts">
                        <Fact label="Primary" value={opStatus.primaryHealthy ? 'Healthy' : 'Down'} good={opStatus.primaryHealthy} />
                        <Fact label="Backup" value={opStatus.backupHealthy ? 'Healthy' : 'Down'} good={opStatus.backupHealthy} />
                        <Fact label="Active path" value={opStatus.activePath} good={opStatus.activePath === 'primary'} />
                      </dl>
                      {opStatus.message && <p className="p2-muted p2-small">{opStatus.message}</p>}
                    </>
                  ) : (
                    <p className="p2-muted p2-small">No report for this node</p>
                  )}

                  <h3>Workload pods</h3>
                  {nodePods.length === 0 && <p className="p2-muted p2-small">None on this node</p>}
                  {nodePods.map(pod => {
                    const restarts = (pod.status.containerStatuses ?? []).reduce((sum: number, c: any) => sum + c.restartCount, 0);
                    return (
                      <div key={pod.metadata.name} className="p2-pod">
                        <span className="p2-small">{pod.metadata.name}</span>
                        <span className="p2-mono p2-small">eth0 {pod.status.podIP ?? 'none'} · net1 {net1IP(pod) ?? 'none'} · restarts {restarts}</span>
                      </div>
                    );
                  })}
                </button>
              );
            })}
          </div>
        </section>

        <section className="p2-panel p2-q-controls">
          <h2>Inject a fault {targetNode && <span className="p2-muted">on {targetNode}</span>}</h2>
          <div className="p2-actions">
            <ActionButton
              icon={<Flame size={16} />}
              title="Block pod traffic"
              detail="Adds an iptables FORWARD DROP for 10.244.0.0/16 on the node"
              tone="danger"
              disabled={!!busy || !targetNode || !!targetFaultRules}
              onClick={() => runAction('Block pod traffic', '/api/fault/cniresilience/break', { nodeName: targetNode, method: 'pod-traffic' })}
            />
            <ActionButton
              icon={<ShieldAlert size={16} />}
              title="Delete calico-node pod"
              detail="Existing routes stay in the kernel; the DaemonSet replaces the pod"
              tone="warn"
              disabled={!!busy || !targetNode}
              onClick={() => runAction('Delete calico-node pod', '/api/fault/cniresilience/break', { nodeName: targetNode, method: 'calico-agent' })}
            />
            <ActionButton
              icon={<RotateCcw size={16} />}
              title="Reset the lab"
              detail="Removes demo rules, the recovery taint and shadow slices on all nodes"
              tone="good"
              disabled={!!busy}
              onClick={() => runAction('Reset the lab', '/api/fault/cniresilience/restore', {})}
            />
          </div>

          <h3>Operator settings <span className="p2-muted">stored in the CR</span></h3>
          <div className="p2-toggles">
            <Toggle
              icon={<Shuffle size={15} />}
              label="Auto failover"
              on={!!cr?.spec?.autoFailover}
              disabled={!cr || !!busy}
              onClick={() => runAction('Toggle auto failover', '/api/fault/cniresilience/config', { autoFailover: !cr?.spec?.autoFailover })}
            />
            <Toggle
              icon={<HeartHandshake size={15} />}
              label="Auto recover"
              on={!!cr?.spec?.autoRecover}
              disabled={!cr || !!busy}
              onClick={() => runAction('Toggle auto recover', '/api/fault/cniresilience/config', { autoRecover: !cr?.spec?.autoRecover })}
            />
          </div>
        </section>

        <section className="p2-panel p2-q-slices">
          <div className="p2-tabs">
            <button className={tab === 'slices' ? 'active' : ''} onClick={() => setTab('slices')}>
              <Layers size={14} /> EndpointSlices
            </button>
            <button className={tab === 'events' ? 'active' : ''} onClick={() => setTab('events')}>
              <TerminalIcon size={14} /> Observed changes {events.length > 0 && `(${events.length})`}
            </button>
          </div>

          {tab === 'slices' ? (
            <div className="p2-scroll">
              {(cluster.endpointSlices ?? []).length === 0 && <p className="p2-muted p2-small">No EndpointSlices for resilience-service.</p>}
              {(cluster.endpointSlices ?? []).map(slice => {
                const shadow = slice.metadata.labels?.['cniresilience.cn-operator/shadow'] === 'true';
                return (
                  <div key={slice.metadata.name} className={`p2-slice ${shadow ? 'shadow' : ''}`}>
                    <div className="p2-slice-head">
                      <span className="p2-mono">{slice.metadata.name}</span>
                      {shadow && <span className="p2-tag warn">SHADOW</span>}
                    </div>
                    {(slice.endpoints ?? []).map((ep: any) => {
                      const ready = ep.conditions?.ready !== false;
                      return (
                        <div key={ep.addresses[0]} className="p2-endpoint">
                          <span className="p2-mono p2-small">{ep.addresses[0]} <span className="p2-muted">{ep.nodeName}</span></span>
                          <span className={`p2-tag ${ready ? 'good' : 'bad'}`}>{ready ? 'ready' : 'not ready'}</span>
                        </div>
                      );
                    })}
                  </div>
                );
              })}
            </div>
          ) : (
            <div className="p2-scroll p2-log">
              {events.length === 0 && <p className="p2-muted">Nothing has changed since this page opened.</p>}
              {[...events].reverse().map((e, i) => (
                <div key={events.length - i} className={`p2-log-line ${e.kind}`}>
                  <span className="p2-muted">{e.time}</span> {e.text}
                </div>
              ))}
            </div>
          )}
        </section>
      </main>
    </div>
  );
}

type Tone = 'good' | 'warn' | 'bad' | 'none';

function Pill({ label, value, tone }: { label: string; value: string; tone: Tone }) {
  return (
    <div className="p2-pill">
      <span className="p2-muted">{label}</span>
      <strong className={`tone-${tone}`}>{value}</strong>
    </div>
  );
}

function Stat({ label, value, tone }: { label: string; value: string; tone: Tone }) {
  return (
    <div className="p2-stat">
      <strong className={`tone-${tone}`}>{value}</strong>
      <span className="p2-muted">{label}</span>
    </div>
  );
}

function Fact({ label, value, good }: { label: string; value: string; good: boolean }) {
  return (
    <div className="p2-fact">
      <dt className="p2-muted">{label}</dt>
      <dd className={good ? 'tone-good' : 'tone-bad'}>
        {good ? <CheckCircle2 size={13} /> : <XCircle size={13} />} {value}
      </dd>
    </div>
  );
}

function ActionButton({ icon, title, detail, tone, disabled, onClick }: {
  icon: React.ReactNode;
  title: string;
  detail: string;
  tone: 'danger' | 'warn' | 'good';
  disabled: boolean;
  onClick: () => void;
}) {
  return (
    <button className={`p2-action ${tone}`} disabled={disabled} onClick={onClick}>
      {icon}
      <span>
        <strong>{title}</strong>
        <span className="p2-muted p2-small">{detail}</span>
      </span>
    </button>
  );
}

function Toggle({ icon, label, on, disabled, onClick }: {
  icon: React.ReactNode;
  label: string;
  on: boolean;
  disabled: boolean;
  onClick: () => void;
}) {
  return (
    <button className={`p2-toggle ${on ? 'on' : ''}`} disabled={disabled} aria-pressed={on} onClick={onClick}>
      {icon} {label}: <strong>{on ? 'On' : 'Off'}</strong>
    </button>
  );
}
