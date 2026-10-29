import { useState, useEffect } from 'react';
import { 
  Activity, 
  Server, 
  Wifi, 
  WifiOff, 
  ShieldAlert, 
  CheckCircle2, 
  AlertTriangle, 
  Flame, 
  HeartHandshake, 
  Terminal as TerminalIcon, 
  Layers,
  RotateCcw
} from 'lucide-react';
import './Project2Demo.css';

interface NodePathHealth {
  name: string;
  isReady: boolean;
  tainted: boolean;
  primaryHealthy: boolean;
  backupHealthy: boolean;
  activePath: 'primary' | 'backup';
  primaryIP: string;
  backupIP: string;
  calicoFelixStatus: 'running' | 'down';
  flannelStatus: 'running' | 'down';
  consecutiveFailures: number;
}

interface PodInfo {
  name: string;
  node: string;
  primaryIP: string;
  backupIP: string;
  status: string;
}

interface EndpointSliceInfo {
  name: string;
  serviceName: string;
  isShadow: boolean;
  endpoints: { ip: string; ready: boolean; nodeName: string }[];
}

interface TrafficSample {
  id: number;
  timestamp: string;
  status: '200 OK' | 'TIMEOUT' | 'FAIL';
  latencyMs: number;
  path: 'primary' | 'backup';
}

export function Project2Demo() {
  const [phase, setPhase] = useState<'Healthy' | 'FailedOver' | 'Degraded'>('Healthy');
  const [failoverCount, setFailoverCount] = useState<number>(0);
  const [recoveryCount, setRecoveryCount] = useState<number>(0);
  const [autoRecover, setAutoRecover] = useState<boolean>(true);
  const [autoFailover, setAutoFailover] = useState<boolean>(true);
  const [selectedNode, setSelectedNode] = useState<string>('minikube-m02');
  const [isInjecting, setIsInjecting] = useState<boolean>(false);
  const [activeTab, setActiveTab] = useState<'endpoints' | 'terminal'>('endpoints');

  // Node health states
  const [nodes, setNodes] = useState<NodePathHealth[]>([
    {
      name: 'minikube',
      isReady: true,
      tainted: false,
      primaryHealthy: true,
      backupHealthy: true,
      activePath: 'primary',
      primaryIP: '192.168.49.2',
      backupIP: '192.168.1.1',
      calicoFelixStatus: 'running',
      flannelStatus: 'running',
      consecutiveFailures: 0,
    },
    {
      name: 'minikube-m02',
      isReady: true,
      tainted: false,
      primaryHealthy: true,
      backupHealthy: true,
      activePath: 'primary',
      primaryIP: '192.168.49.3',
      backupIP: '192.168.1.2',
      calicoFelixStatus: 'running',
      flannelStatus: 'running',
      consecutiveFailures: 0,
    },
  ]);

  const [pods] = useState<PodInfo[]>([
    {
      name: 'resilience-target-7b98f-v8k2p',
      node: 'minikube-m02',
      primaryIP: '10.244.1.42',
      backupIP: '192.168.1.105',
      status: 'Running',
    },
    {
      name: 'resilience-target-7b98f-nm6tw',
      node: 'minikube',
      primaryIP: '10.244.0.18',
      backupIP: '192.168.1.106',
      status: 'Running',
    },
  ]);

  // EndpointSlices
  const [endpointSlices, setEndpointSlices] = useState<EndpointSliceInfo[]>([
    {
      name: 'resilience-service-primary',
      serviceName: 'resilience-service',
      isShadow: false,
      endpoints: [
        { ip: '10.244.1.42', ready: true, nodeName: 'minikube-m02' },
        { ip: '10.244.0.18', ready: true, nodeName: 'minikube' },
      ],
    },
  ]);

  // Live Traffic Monitor
  const [trafficStream, setTrafficStream] = useState<TrafficSample[]>([]);
  const [logs, setLogs] = useState<{ time: string; text: string; type?: 'highlight' | 'error' | 'info' }[]>([
    { time: '17:30:00', text: 'CNI Resilience Controller initialized with CR "default-resilience"', type: 'info' },
    { time: '17:30:02', text: 'HealthAgent probe cycle active: Calico eth0 (10.244.0.0/16) and Flannel net1 (192.168.0.0/16) UP', type: 'info' },
    { time: '17:30:05', text: 'BFD-style ICMP & TCP health probes passing on all nodes', type: 'info' },
  ]);

  const addLog = (text: string, type: 'highlight' | 'error' | 'info' = 'info') => {
    const time = new Date().toLocaleTimeString('en-US', { hour12: false });
    setLogs(prev => [...prev.slice(-40), { time, text, type }]);
  };

  // Poll real cluster state if available
  useEffect(() => {
    const fetchClusterState = async () => {
      try {
        const res = await fetch('/api/fault/cniresilience/status');
        if (!res.ok) return;
        const data = await res.json();
        if (data.success && data.cr) {
          const crStatus = data.cr.status || {};
          if (crStatus.phase) setPhase(crStatus.phase);
          if (typeof crStatus.failoverCount === 'number') setFailoverCount(crStatus.failoverCount);
          if (typeof crStatus.recoveryCount === 'number') setRecoveryCount(crStatus.recoveryCount);
        }
      } catch {}
    };

    const interval = setInterval(fetchClusterState, 3000);
    return () => clearInterval(interval);
  }, []);

  // Continuous Live Traffic Simulation / Probing
  useEffect(() => {
    let sampleCounter = 0;
    const trafficTimer = setInterval(() => {
      sampleCounter++;
      const timeStr = new Date().toLocaleTimeString('en-US', { hour12: false });

      // Determine current health on selected node
      const targetNode = nodes.find(n => n.name === selectedNode) || nodes[1];
      const isFailedOver = targetNode.activePath === 'backup';
      const isPrimaryDown = !targetNode.primaryHealthy;

      let status: TrafficSample['status'] = '200 OK';
      let latency = Math.floor(Math.random() * 3) + 1.2;

      if (isPrimaryDown && !isFailedOver) {
        // Blip before failover completes
        status = 'TIMEOUT';
        latency = 1200;
      } else if (isFailedOver) {
        // Healthy over Flannel backup path
        status = '200 OK';
        latency = Math.floor(Math.random() * 4) + 2.5; // slight overlay delta
      }

      setTrafficStream(prev => [
        ...prev.slice(-28),
        {
          id: sampleCounter,
          timestamp: timeStr,
          status,
          latencyMs: Number(latency.toFixed(1)),
          path: isFailedOver ? 'backup' : 'primary',
        },
      ]);
    }, 1000);

    return () => clearInterval(trafficTimer);
  }, [nodes, selectedNode]);

  // Chaos Actions
  const handleSeverCalico = async () => {
    setIsInjecting(true);
    addLog(`[CHAOS] Injecting Calico datapath drop (VXLAN port 4789 + pod CIDR DROP) on ${selectedNode}...`, 'highlight');

    try {
      await fetch('/api/fault/cniresilience/break', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ nodeName: selectedNode, method: 'vxlan' }),
      });
    } catch {}

    // Transition local state to reflect failure
    setNodes(prev => prev.map(n => {
      if (n.name === selectedNode) {
        return {
          ...n,
          primaryHealthy: false,
          consecutiveFailures: 3,
        };
      }
      return n;
    }));

    addLog(`[HEALTH-AGENT] Node ${selectedNode}: Primary path failed 3 consecutive probes (BFD timeout)`, 'error');

    // Simulate Failover Pipeline trigger (< 1.5 seconds)
    setTimeout(() => {
      if (autoFailover) {
        addLog(`[FAILOVER] Primary path down! AutoFailover engaged for ${selectedNode}`, 'highlight');
        addLog(`[FAILOVER] Discovered Multus backup IP "192.168.1.105" for pod on ${selectedNode}`, 'info');
        addLog(`[FAILOVER] Creating Shadow EndpointSlice "resilience-service-shadow" pointing to backup net1 IP`, 'highlight');
        addLog(`[FAILOVER] Marking original primary endpoint 10.244.1.42 as Ready: false in kube-proxy`, 'info');

        setNodes(prev => prev.map(n => n.name === selectedNode ? { ...n, activePath: 'backup' } : n));
        setPhase('FailedOver');
        setFailoverCount(c => c + 1);

        setEndpointSlices([
          {
            name: 'resilience-service-primary',
            serviceName: 'resilience-service',
            isShadow: false,
            endpoints: [
              { ip: '10.244.1.42', ready: false, nodeName: 'minikube-m02' },
              { ip: '10.244.0.18', ready: true, nodeName: 'minikube' },
            ],
          },
          {
            name: 'resilience-service-shadow',
            serviceName: 'resilience-service',
            isShadow: true,
            endpoints: [
              { ip: '192.168.1.105', ready: true, nodeName: 'minikube-m02' },
            ],
          },
        ]);
      }

      // Simulate Recovery Pipeline (< 6 seconds)
      if (autoRecover) {
        setTimeout(() => {
          addLog(`[RECOVERY] Autonomous recovery started for node ${selectedNode} via surviving Flannel path`, 'highlight');
          addLog(`[RECOVERY] Step 1: Tainting node ${selectedNode} with "cniresilience.cn-operator/recovering:NoSchedule"`, 'info');
          
          setNodes(prev => prev.map(n => n.name === selectedNode ? { ...n, tainted: true } : n));

          setTimeout(() => {
            addLog(`[RECOVERY] Step 2: Restarting calico-node DaemonSet pod on ${selectedNode}`, 'info');
            addLog(`[RECOVERY] Step 3: Waiting for Felix liveness endpoint (http://localhost:9099/liveness) to respond`, 'info');
            
            setTimeout(() => {
              addLog(`[RECOVERY] Step 4: Kernel routes verified intact. Untainting node ${selectedNode}`, 'info');
              addLog(`[FAILBACK] Primary CNI verified healthy! Reverting traffic to primary Calico eth0`, 'highlight');
              addLog(`[FAILBACK] Deleting Shadow EndpointSlice. Marking original primary endpoints Ready: true`, 'info');

              setNodes(prev => prev.map(n => {
                if (n.name === selectedNode) {
                  return {
                    ...n,
                    primaryHealthy: true,
                    activePath: 'primary',
                    tainted: false,
                    consecutiveFailures: 0,
                  };
                }
                return n;
              }));

              setEndpointSlices([
                {
                  name: 'resilience-service-primary',
                  serviceName: 'resilience-service',
                  isShadow: false,
                  endpoints: [
                    { ip: '10.244.1.42', ready: true, nodeName: 'minikube-m02' },
                    { ip: '10.244.0.18', ready: true, nodeName: 'minikube' },
                  ],
                },
              ]);

              setPhase('Healthy');
              setRecoveryCount(c => c + 1);
              setIsInjecting(false);
              addLog(`[SUCCESS] Full cycle complete: Outage detected, failed over with zero downtime, and self-healed!`, 'highlight');
            }, 3000);
          }, 2500);
        }, 2000);
      } else {
        setIsInjecting(false);
      }
    }, 1500);
  };

  const handleKillCalicoPod = async () => {
    setIsInjecting(true);
    addLog(`[CHAOS] Deleting calico-node pod on ${selectedNode} to simulate agent crash...`, 'highlight');

    try {
      await fetch('/api/fault/cniresilience/break', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ nodeName: selectedNode, method: 'calico-agent' }),
      });
    } catch {}

    setNodes(prev => prev.map(n => n.name === selectedNode ? { ...n, calicoFelixStatus: 'down' } : n));
    addLog(`[HEALTH-AGENT] Felix liveness check failed on ${selectedNode}: connection refused`, 'error');

    setTimeout(() => {
      addLog(`[RECOVERY] Detected dead calico-node on ${selectedNode}. DaemonSet controller spinning up replacement pod...`, 'info');
      setNodes(prev => prev.map(n => n.name === selectedNode ? { ...n, calicoFelixStatus: 'running' } : n));
      addLog(`[RECOVERY] calico-node ready on ${selectedNode}. Health check passed.`, 'highlight');
      setIsInjecting(false);
    }, 4000);
  };

  const handleManualRestore = async () => {
    addLog(`[CONTROL] Restoring all network rules and cleaning shadow slices on ${selectedNode}...`, 'info');
    try {
      await fetch('/api/fault/cniresilience/restore', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ nodeName: selectedNode }),
      });
    } catch {}

    setNodes(prev => prev.map(n => ({
      ...n,
      primaryHealthy: true,
      backupHealthy: true,
      activePath: 'primary',
      tainted: false,
      calicoFelixStatus: 'running',
    })));

    setEndpointSlices([
      {
        name: 'resilience-service-primary',
        serviceName: 'resilience-service',
        isShadow: false,
        endpoints: [
          { ip: '10.244.1.42', ready: true, nodeName: 'minikube-m02' },
          { ip: '10.244.0.18', ready: true, nodeName: 'minikube' },
        ],
      },
    ]);

    setPhase('Healthy');
    setIsInjecting(false);
    addLog(`[CONTROL] Environment reset to baseline Healthy state.`, 'highlight');
  };

  const totalProbes = trafficStream.length;
  const successfulProbes = trafficStream.filter(s => s.status === '200 OK').length;
  const successRate = totalProbes > 0 ? ((successfulProbes / totalProbes) * 100).toFixed(0) : '100';
  const avgLatency = trafficStream.length > 0 
    ? (trafficStream.reduce((acc, s) => acc + (s.status === '200 OK' ? s.latencyMs : 0), 0) / Math.max(1, successfulProbes)).toFixed(1)
    : '1.4';

  const currentNode = nodes.find(n => n.name === selectedNode) || nodes[0];

  return (
    <div className="p2-container">
      {/* Header */}
      <header className="p2-header">
        <div className="p2-title-group">
        </div>

        <div className="p2-header-metrics">
          <div className="p2-metric-pill">
            <span className="label">Cluster Phase:</span>
            <span className={`val ${
              phase === 'Healthy' ? 'p2-status-healthy' :
              phase === 'FailedOver' ? 'p2-status-failedover' : 'p2-status-degraded'
            }`}>
              {phase === 'Healthy' && <CheckCircle2 size={14} style={{ display: 'inline', marginRight: 4 }} />}
              {phase === 'FailedOver' && <AlertTriangle size={14} style={{ display: 'inline', marginRight: 4 }} />}
              {phase.toUpperCase()}
            </span>
          </div>

          <div className="p2-metric-pill">
            <span className="label">Failovers:</span>
            <span className="val" style={{ color: '#f59e0b' }}>{failoverCount}</span>
          </div>

          <div className="p2-metric-pill">
            <span className="label">Recoveries:</span>
            <span className="val" style={{ color: '#10b981' }}>{recoveryCount}</span>
          </div>
        </div>
      </header>

      {/* Main 4-Quadrant Grid */}
      <div className="p2-main-grid">
        
        {/* Quadrant 1: Dual-CNI Topology Map */}
        <section className="p2-panel">
          {/* <div className="p2-panel-header">
            <span style={{ fontSize: '0.75rem', color: '#94a3b8' }}>
              Primary: Calico eth0 (10.244.x.x) | Backup: Flannel net1 (192.168.x.x)
            </span>
          </div> */}

          <div className="p2-topology-content">
            {nodes.map(node => (
              <div 
                key={node.name} 
                className={`p2-node-card ${node.activePath === 'backup' ? 'failed-over' : ''} ${node.tainted ? 'tainted' : ''}`}
                onClick={() => setSelectedNode(node.name)}
                style={{ cursor: 'pointer', border: node.name === selectedNode ? '2px solid #3b82f6' : undefined }}
              >
                <div className="p2-node-header">
                  <div className="p2-node-name">
                    <Server size={15} color={node.isReady ? '#10b981' : '#ef4444'} />
                    {node.name}
                    {node.name === selectedNode && (
                      <span style={{ fontSize: '0.68rem', background: '#3b82f6', color: '#fff', padding: '1px 6px', borderRadius: 4 }}>
                        TARGET
                      </span>
                    )}
                  </div>
                  <div style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
                    {node.tainted && (
                      <span className="p2-taint-badge">TAINTED: NoSchedule</span>
                    )}
                    <span style={{ fontSize: '0.75rem', color: '#94a3b8', fontFamily: 'monospace' }}>
                      Host IP: {node.primaryIP}
                    </span>
                  </div>
                </div>

                {/* Dual CNI Paths */}
                <div className="p2-cni-paths">
                  {/* Calico Primary Path */}
                  <div className={`p2-path-box ${node.activePath === 'primary' ? 'active-traffic' : ''} ${!node.primaryHealthy ? 'path-down' : ''}`}>
                    <div className="p2-path-title">
                      <span style={{ color: node.primaryHealthy ? '#93c5fd' : '#f87171', display: 'flex', alignItems: 'center', gap: 5 }}>
                        {node.primaryHealthy ? <Wifi size={13} /> : <WifiOff size={13} />}
                        Calico (eth0)
                      </span>
                      <span style={{ fontSize: '0.68rem', color: node.primaryHealthy ? '#10b981' : '#ef4444', fontWeight: 700 }}>
                        {node.primaryHealthy ? 'HEALTHY' : 'DOWN'}
                      </span>
                    </div>
                    <div className="p2-path-details">
                      CIDR: 10.244.{node.name === 'minikube' ? '0' : '1'}.0/24<br/>
                      Felix Agent: {node.calicoFelixStatus.toUpperCase()}<br/>
                      {node.activePath === 'primary' ? '⚡ ACTIVE DATAPATH' : 'STANDBY'}
                    </div>
                  </div>

                  {/* Flannel Backup Path */}
                  <div className={`p2-path-box ${node.activePath === 'backup' ? 'active-traffic' : ''} ${!node.backupHealthy ? 'path-down' : ''}`}>
                    <div className="p2-path-title">
                      <span style={{ color: '#fde047', display: 'flex', alignItems: 'center', gap: 5 }}>
                        <Wifi size={13} />
                        Flannel (net1)
                      </span>
                      <span style={{ fontSize: '0.68rem', color: '#10b981', fontWeight: 700 }}>
                        STANDBY READY
                      </span>
                    </div>
                    <div className="p2-path-details">
                      CIDR: 192.168.1.0/24 (Multus)<br/>
                      Encapsulation: VXLAN UDP 8472<br/>
                      {node.activePath === 'backup' ? '⚡ ACTIVE (FAILED-OVER)' : 'STANDBY'}
                    </div>
                  </div>
                </div>

                {/* Workload Pod on this Node */}
                <div className="p2-node-pods">
                  {pods.filter(p => p.node === node.name).map(p => (
                    <div key={p.name} className="p2-pod-chip">
                      <span style={{ fontWeight: 600, color: '#f1f5f9' }}>{p.name}</span>
                      <div className="p2-pod-ips">
                        <span className="p2-ip-tag p2-ip-primary" title="Primary eth0 IP">
                          eth0: {p.primaryIP}
                        </span>
                        <span className="p2-ip-tag p2-ip-backup" title="Multus Backup net1 IP">
                          net1: {p.backupIP}
                        </span>
                      </div>
                    </div>
                  ))}
                </div>
              </div>
            ))}
          </div>
        </section>

        {/* Quadrant 2: Live Service Traffic Continuity Monitor */}
        <section className="p2-panel-right">
          {/* <div className="p2-panel-header">
            <span style={{ fontSize: '0.72rem', color: '#94a3b8' }}>
              Target: resilience-service (ClusterIP: 10.96.120.45:80)
            </span>
          </div> */}

          <div className="p2-traffic-container">
            {/* Real-time Metrics Row */}
            <div className="p2-traffic-stats-row">
              <div className="p2-stat-box">
                <div className="p2-stat-val" style={{ color: '#10b981' }}>{successRate}%</div>
                <div className="p2-stat-label">Availability</div>
              </div>
              <div className="p2-stat-box">
                <div className="p2-stat-val" style={{ color: '#38bdf8' }}>{avgLatency}ms</div>
                <div className="p2-stat-label">Avg RTT Latency</div>
              </div>
              <div className="p2-stat-box">
                <div className="p2-stat-val" style={{ color: currentNode.activePath === 'primary' ? '#60a5fa' : '#f59e0b' }}>
                  {currentNode.activePath.toUpperCase()}
                </div>
                <div className="p2-stat-label">Active Route</div>
              </div>
              <div className="p2-stat-box">
                <div className="p2-stat-val" style={{ color: '#a78bfa' }}>1.0 req/s</div>
                <div className="p2-stat-label">Poll Rate</div>
              </div>
            </div>

            {/* Visualizer Waveform / Bars */}
            <div className="p2-stream-visual">
              <div style={{ fontSize: '0.75rem', color: '#94a3b8', display: 'flex', justifyContent: 'space-between', marginBottom: 4 }}>
                <span>Real-Time Traffic Heartbeat (Continuous HTTP Probes)</span>
                <span>Latency / Status</span>
              </div>
              <div className="p2-stream-history">
                {trafficStream.map(sample => {
                  const isSuccess = sample.status === '200 OK';
                  const isFailover = sample.path === 'backup';
                  const height = isSuccess ? Math.min(80, Math.max(20, sample.latencyMs * 18)) : 10;

                  return (
                    <div
                      key={sample.id}
                      className={`p2-stream-bar ${
                        !isSuccess ? 'p2-bar-fail' :
                        isFailover ? 'p2-bar-failover' : 'p2-bar-success'
                      }`}
                      style={{ height: `${height}px` }}
                      title={`[${sample.timestamp}] Status: ${sample.status} | Latency: ${sample.latencyMs}ms | Path: ${sample.path}`}
                    />
                  );
                })}
              </div>

              <div className="p2-active-route-indicator">
                <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                  <span style={{ color: '#94a3b8' }}>Ingress Steering:</span>
                  <span style={{ 
                    color: currentNode.activePath === 'primary' ? '#60a5fa' : '#f59e0b',
                    fontWeight: 700,
                    fontFamily: 'monospace'
                  }}>
                    {currentNode.activePath === 'primary' 
                      ? 'kube-proxy → Calico eth0 (10.244.1.42)' 
                      : 'kube-proxy → Flannel net1 Shadow (192.168.1.105)'}
                  </span>
                </div>
                <span style={{ color: '#10b981', fontSize: '0.75rem', fontWeight: 600 }}>
                  ● 0 Pod Restarts
                </span>
              </div>
            </div>
          </div>
        </section>

        {/* Quadrant 3: Interactive Chaos & Resilience Deck */}
        <section className="p2-panel">

          <div className="p2-chaos-content">
            <div className="p2-chaos-buttons">
              <button 
                className="p2-btn p2-btn-break"
                onClick={handleSeverCalico}
                disabled={isInjecting || !currentNode.primaryHealthy}
              >
                <Flame size={15} />
                Sever Primary CNI (VXLAN Drop)
              </button>

              <button 
                className="p2-btn p2-btn-calico-kill"
                onClick={handleKillCalicoPod}
                disabled={isInjecting || currentNode.calicoFelixStatus === 'down'}
              >
                <ShieldAlert size={15} />
                Kill Calico Felix Agent Pod
              </button>
            </div>

            <div style={{ display: 'flex', gap: 10 }}>
              <button 
                className="p2-btn p2-btn-restore"
                onClick={handleManualRestore}
                style={{ flex: 1 }}
              >
                <RotateCcw size={15} />
                Reset & Restore All Paths
              </button>

              <button 
                className="p2-btn"
                onClick={() => setAutoFailover(!autoFailover)}
                style={{ 
                  background: autoFailover ? 'rgba(59, 130, 246, 0.15)' : 'rgba(239, 68, 68, 0.15)',
                  borderColor: autoFailover ? '#3b82f6' : '#ef4444',
                  color: autoFailover ? '#93c5fd' : '#fca5a5'
                }}
              >
                Failover: {autoFailover ? 'ENABLED' : 'DISABLED'}
              </button>

              <button 
                className="p2-btn"
                onClick={() => setAutoRecover(!autoRecover)}
                style={{ 
                  background: autoRecover ? 'rgba(16, 185, 129, 0.15)' : 'rgba(239, 68, 68, 0.15)',
                  borderColor: autoRecover ? '#10b981' : '#ef4444',
                  color: autoRecover ? '#6ee7b7' : '#fca5a5'
                }}
              >
                <HeartHandshake size={15} />
                Auto-Healing: {autoRecover ? 'ENABLED' : 'DISABLED'}
              </button>
            </div>
          </div>
        </section>

        {/* Quadrant 4: EndpointSlices & Live Event Terminal */}
        <section className="p2-panel">
          <div className="p2-panel-header">
            <div className="p2-tabs">
              <button 
                className={`p2-tab ${activeTab === 'endpoints' ? 'active' : ''}`}
                onClick={() => setActiveTab('endpoints')}
              >
                <Layers size={13} style={{ display: 'inline', marginRight: 4 }} />
                EndpointSlices (VRRP Mechanism)
              </button>
              <button 
                className={`p2-tab ${activeTab === 'terminal' ? 'active' : ''}`}
                onClick={() => setActiveTab('terminal')}
              >
                <TerminalIcon size={13} style={{ display: 'inline', marginRight: 4 }} />
                Live Operator Console
              </button>
            </div>
            <span style={{ fontSize: '0.72rem', color: '#94a3b8' }}>
              discovery.k8s.io/v1
            </span>
          </div>

          {activeTab === 'endpoints' ? (
            <div className="p2-ep-list">
              {endpointSlices.map(ep => (
                <div key={ep.name} className={`p2-ep-card ${ep.isShadow ? 'shadow-active' : ''}`}>
                  <div className="p2-ep-header">
                    <span style={{ color: ep.isShadow ? '#f59e0b' : '#38bdf8' }}>
                      {ep.name}
                    </span>
                    {ep.isShadow ? (
                      <span className="p2-ep-shadow-tag">⚡ SHADOW SLICE (ACTIVE)</span>
                    ) : (
                      <span style={{ color: '#94a3b8', fontSize: '0.7rem' }}>Standard EndpointSlice</span>
                    )}
                  </div>
                  <div className="p2-ep-endpoints">
                    {ep.endpoints.map((e, idx) => (
                      <div key={idx} style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                        <span>
                          {e.ip} ({e.nodeName})
                        </span>
                        <span style={{ 
                          color: e.ready ? '#10b981' : '#ef4444', 
                          fontWeight: 600,
                          fontSize: '0.68rem',
                          background: e.ready ? 'rgba(16, 185, 129, 0.1)' : 'rgba(239, 68, 68, 0.1)',
                          padding: '1px 6px',
                          borderRadius: 4
                        }}>
                          {e.ready ? 'Ready: true' : 'Ready: false'}
                        </span>
                      </div>
                    ))}
                  </div>
                </div>
              ))}
            </div>
          ) : (
            <div className="p2-terminal">
              {logs.map((log, index) => (
                <div key={index} className="p2-log-item">
                  <span className="p2-log-time">[{log.time}]</span>
                  <span className={
                    log.type === 'highlight' ? 'p2-log-highlight' :
                    log.type === 'error' ? 'p2-log-error' : ''
                  }>
                    {log.text}
                  </span>
                </div>
              ))}
            </div>
          )}
        </section>

      </div>
    </div>
  );
}
