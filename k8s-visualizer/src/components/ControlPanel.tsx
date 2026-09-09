import React, { useState } from 'react';
import { ShieldAlert, Plus, Trash2, Terminal, CheckCircle, XCircle, Zap, Cpu, Network, WifiOff, FileCode } from 'lucide-react';
import type { K8sNode, K8sPod, OperatorStatus } from '../hooks/useKubernetes';

interface ControlPanelProps {
  onLog: (msg: string, type?: 'info'|'warning'|'error'|'success'|'system'|'critical') => void;
  selectedPod: string | null;
  k8sNodes: K8sNode[];
  k8sPods: K8sPod[];
  operatorStatus: OperatorStatus | null;
  k8sApi: any;
  onOpenLogs?: () => void;
}

const DEFAULT_COREFILE = `.:53 {
    errors
    health {
       lameduck 5s
    }
    ready
    kubernetes cluster.local in-addr.arpa ip6.arpa {
       pods insecure
       fallthrough in-addr.arpa ip6.arpa
       ttl 30
    }
    prometheus :9153
    forward . /etc/resolv.conf {
       max_concurrent 1000
    }
    cache 30
    loop
    reload
    loadbalance
}`;

const CORRUPTED_UPSTREAM_COREFILE = `.:53 {
    errors
    health {
       lameduck 5s
    }
    ready
    kubernetes cluster.local in-addr.arpa ip6.arpa {
       pods insecure
       fallthrough in-addr.arpa ip6.arpa
       ttl 30
    }
    prometheus :9153
    forward . 192.0.2.1 {
       max_concurrent 1000
    }
    cache 30
    loop
    reload
    loadbalance
}`;

const BAD_PLUGIN_COREFILE = `.:53 {
    errors
    invalid_plugin_directive_xyz
    health
    ready
    kubernetes cluster.local in-addr.arpa ip6.arpa
    forward . /etc/resolv.conf
}`;

export function ControlPanel({ onLog, selectedPod, k8sNodes, k8sPods, operatorStatus, k8sApi, onOpenLogs }: ControlPanelProps) {
  const [newAppName, setNewAppName] = useState('');
  const [selectedNodeForDeploy, setSelectedNodeForDeploy] = useState<string>('');
  const [isProcessing, setIsProcessing] = useState(false);

  const selectedPodObj = k8sPods.find(p => p.name === selectedPod);

  const handleDeploy = () => {
    if (newAppName && k8sApi.deployCustomApp) {
      onLog(`kubectl create deployment ${newAppName} --image=nginx:alpine`, 'system');
      k8sApi.deployCustomApp(newAppName, selectedNodeForDeploy || undefined);
      setNewAppName('');
    }
  };

  const handleDelete = () => {
    if (selectedPodObj && !selectedPodObj.isCNI && !selectedPodObj.isCoreDNS) {
      onLog(`kubectl delete deployment ${selectedPodObj.appLabel} -n ${selectedPodObj.namespace}`, 'system');
      k8sApi.deleteCustomApp(selectedPodObj.appLabel);
    }
  };

  // --- MODULE 1: CNI Subsystem (E1 - E3) ---
  const exp1KillCNI = async () => {
    if (!selectedPodObj || !selectedPodObj.isCNI) return;
    setIsProcessing(true);
    try {
      const cmd = await k8sApi.deletePod(selectedPodObj.namespace, selectedPodObj.name);
      onLog(cmd, 'system');
      onLog(`[E1 CNI Crash] Killed CNI agent on node ${selectedPodObj.nodeName}. Watch for BGP route loss!`, 'warning');
    } catch (e: any) {
      onLog(`Failed: ${e.message}`, 'error');
    }
    setIsProcessing(false);
  };

  const exp2ExhaustIPAM = async () => {
    setIsProcessing(true);
    try {
      onLog(`kubectl create deployment ipam-filler --image=nginx:alpine --replicas=20`, 'system');
      await k8sApi.deployCustomApp('ipam-filler');
      onLog(`[E2 IPAM Exhaustion] Scaled deployment to fill IPPool. Pods will become stuck in ContainerCreating!`, 'critical');
    } catch (e: any) {
      onLog(`Failed: ${e.message}`, 'error');
    }
    setIsProcessing(false);
  };

  const exp2CleanupIPAM = async () => {
    setIsProcessing(true);
    try {
      onLog(`kubectl delete deployment ipam-filler`, 'system');
      await k8sApi.deleteCustomApp('ipam-filler');
      onLog(`[E2 IPAM Cleanup] Cleaned up filler pods.`, 'success');
    } catch (e: any) {
      onLog(`Note: ${e.message}`, 'info');
    }
    setIsProcessing(false);
  };

  const exp3ToggleIPPool = async (disable: boolean) => {
    setIsProcessing(true);
    try {
      const cmd = await k8sApi.setIPPoolDisabled(disable);
      onLog(cmd, 'system');
      if (disable) {
        onLog(`[E3 IPPool Disabled] Patched IPPool spec.disabled = true. New IP allocations will fail!`, 'critical');
      } else {
        onLog(`[E3 IPPool Enabled] Re-enabled IPPool spec.disabled = false.`, 'success');
      }
    } catch (e: any) {
      onLog(`Failed to patch IPPool: ${e.message}`, 'error');
    }
    setIsProcessing(false);
  };

  // --- MODULE 2: CoreDNS Subsystem (E4 - E7) ---
  const exp4ScaleCoreDNS = async (replicas: number) => {
    setIsProcessing(true);
    try {
      const cmd = await k8sApi.scaleDeployment('kube-system', 'coredns', replicas);
      onLog(cmd, 'system');
      if (replicas === 0) {
        onLog(`[E4 CoreDNS Scale 0] CoreDNS scaled to 0. All cluster DNS resolution will fail!`, 'critical');
      } else {
        onLog(`[E4 CoreDNS Scale Restore] Restored CoreDNS to ${replicas} replicas.`, 'success');
      }
    } catch (e: any) {
      onLog(`Failed to scale CoreDNS: ${e.message}`, 'error');
    }
    setIsProcessing(false);
  };

  const exp5ThrottleCPU = async (throttle: boolean) => {
    setIsProcessing(true);
    try {
      const cmd = await k8sApi.patchDeploymentResources('kube-system', 'coredns', throttle ? '5m' : null);
      onLog(cmd, 'system');
      if (throttle) {
        onLog(`[E5 CPU Throttle] CoreDNS CPU limit set to 5m. Query latency will spike >1000ms!`, 'warning');
      } else {
        onLog(`[E5 CPU Throttle] CoreDNS CPU limits restored.`, 'success');
      }
    } catch (e: any) {
      onLog(`Failed to patch resources: ${e.message}`, 'error');
    }
    setIsProcessing(false);
  };

  const exp6CorruptUpstream = async (corrupt: boolean) => {
    setIsProcessing(true);
    try {
      const cmd = await k8sApi.updateCoreDNSConfigMap(corrupt ? CORRUPTED_UPSTREAM_COREFILE : DEFAULT_COREFILE);
      onLog(cmd, 'system');
      if (corrupt) {
        onLog(`[E6 Upstream Corrupt] Forward target set to 192.0.2.1. External DNS resolution will hang!`, 'critical');
      } else {
        onLog(`[E6 Upstream Restored] Restored valid Corefile.`, 'success');
      }
    } catch (e: any) {
      onLog(`Failed: ${e.message}`, 'error');
    }
    setIsProcessing(false);
  };

  const exp7CrashLoop = async (crash: boolean) => {
    setIsProcessing(true);
    try {
      const cmd = await k8sApi.updateCoreDNSConfigMap(crash ? BAD_PLUGIN_COREFILE : DEFAULT_COREFILE);
      onLog(cmd, 'system');
      if (crash) {
        onLog(`[E7 Bad Plugin] Injected invalid directive into Corefile. CoreDNS pods will CrashLoop!`, 'critical');
      } else {
        onLog(`[E7 Corefile Restored] Restored valid Corefile.`, 'success');
      }
    } catch (e: any) {
      onLog(`Failed: ${e.message}`, 'error');
    }
    setIsProcessing(false);
  };

  // --- MODULE 3: NetworkPolicy Enforcement (E8) ---
  const exp8FelixDrift = async () => {
    if (!selectedPodObj || selectedPodObj.isCNI || selectedPodObj.isCoreDNS) return;
    setIsProcessing(true);
    try {
      const policy = {
        apiVersion: 'networking.k8s.io/v1',
        kind: 'NetworkPolicy',
        metadata: { name: 'deny-all-exp8', namespace: selectedPodObj.namespace },
        spec: {
          podSelector: { matchLabels: { app: selectedPodObj.appLabel } },
          policyTypes: ['Ingress', 'Egress']
        }
      };
      const cmd = await k8sApi.applyNetworkPolicy(selectedPodObj.namespace, 'deny-all-exp8', policy);
      onLog(cmd, 'system');
      onLog(`[E8 Felix Drift] Applied Deny-All policy to ${selectedPodObj.appLabel}. Now crash Felix to observe policy drift!`, 'critical');
    } catch (e: any) {
      onLog(`Failed: ${e.message}`, 'error');
    }
    setIsProcessing(false);
  };

  const exp8RemovePolicy = async () => {
    setIsProcessing(true);
    try {
      if (selectedPodObj) {
        const cmd = await k8sApi.deleteNetworkPolicy(selectedPodObj.namespace, 'deny-all-exp8');
        onLog(cmd, 'system');
        onLog(`[E8 Felix Drift] Removed Deny-All NetworkPolicy.`, 'success');
      }
    } catch (e: any) {
      onLog(`Note: Policy already removed.`, 'info');
    }
    setIsProcessing(false);
  };

  // --- MODULE 4: Pod-to-Pod Connectivity (E9 - E12) ---
  const exp9DownVeth = async () => {
    if (!selectedPodObj || !selectedPodObj.nodeName) return;
    setIsProcessing(true);
    try {
      const downCmd = `ip link | grep veth | awk '{print $2}' | cut -d: -f1 | head -n 1 | xargs -I {} ip link set {} down`;
      const cmd = await k8sApi.runNodeShellCommand(selectedPodObj.nodeName, downCmd);
      onLog(cmd, 'system');
      onLog(`[E9 Local veth Down] Downed local veth interface on node ${selectedPodObj.nodeName}.`, 'critical');
    } catch (e: any) {
      onLog(`Failed: ${e.message}`, 'error');
    }
    setIsProcessing(false);
  };

  const exp10ToggleTunnel = async (down: boolean) => {
    if (!selectedPodObj || !selectedPodObj.nodeName) return;
    setIsProcessing(true);
    try {
      const tunnelCmd = down ? `ip link set tunl0 down` : `ip link set tunl0 up`;
      const cmd = await k8sApi.runNodeShellCommand(selectedPodObj.nodeName, tunnelCmd);
      onLog(cmd, 'system');
      if (down) {
        onLog(`[E10 Overlay Tunnel Down] Downed tunl0 interface on ${selectedPodObj.nodeName}. Cross-node pod traffic will drop!`, 'critical');
      } else {
        onLog(`[E10 Overlay Tunnel Restored] Brought tunl0 interface back UP.`, 'success');
      }
    } catch (e: any) {
      onLog(`Failed: ${e.message}`, 'error');
    }
    setIsProcessing(false);
  };

  const exp11ToggleIptables = async (drop: boolean) => {
    if (!selectedPodObj || !selectedPodObj.nodeName) return;
    setIsProcessing(true);
    try {
      const iptablesCmd = drop 
        ? `iptables -I FORWARD -j DROP -m comment --comment "exp11-failure"`
        : `iptables -D FORWARD -j DROP -m comment --comment "exp11-failure"`;
      const cmd = await k8sApi.runNodeShellCommand(selectedPodObj.nodeName, iptablesCmd);
      onLog(cmd, 'system');
      if (drop) {
        onLog(`[E11 Host iptables DROP] Executed iptables FORWARD DROP on ${selectedPodObj.nodeName}. Forwarded packets dropped!`, 'critical');
      } else {
        onLog(`[E11 Host iptables Restored] Restored iptables rules on ${selectedPodObj.nodeName}.`, 'success');
      }
    } catch (e: any) {
      onLog(`Failed: ${e.message}`, 'error');
    }
    setIsProcessing(false);
  };

  const exp12ToggleInterNode = async (breakRoute: boolean) => {
    if (!selectedPodObj || !selectedPodObj.nodeName) return;
    setIsProcessing(true);
    try {
      const routeCmd = breakRoute
        ? `iptables -I OUTPUT -p ipencap -j DROP`
        : `iptables -D OUTPUT -p ipencap -j DROP`;
      const cmd = await k8sApi.runNodeShellCommand(selectedPodObj.nodeName, routeCmd);
      onLog(cmd, 'system');
      if (breakRoute) {
        onLog(`[E12 Inter-Node Break] Blocked IPIP encapsulation traffic on ${selectedPodObj.nodeName}. Inter-node ring probes will fail!`, 'critical');
      } else {
        onLog(`[E12 Inter-Node Restored] Unblocked IPIP traffic on ${selectedPodObj.nodeName}.`, 'success');
      }
    } catch (e: any) {
      onLog(`Failed: ${e.message}`, 'error');
    }
    setIsProcessing(false);
  };

  // Utility Ping Test
  const runPingTest = async () => {
    if (!selectedPodObj) return;
    setIsProcessing(true);
    try {
      onLog(`kubectl exec -it ${selectedPodObj.name} -- ping -c 3 8.8.8.8`, 'system');
      onLog(`Running real ping test... please wait 3-4 seconds.`, 'info');
      const output = await k8sApi.runCommandAndGetLogs(`ping -c 3 8.8.8.8`, selectedPodObj.appLabel);
      onLog(`Ping Output:\n${output}`, 'info');
    } catch(e: any) {
      onLog(`Ping failed: ${e.message}`, 'error');
    }
    setIsProcessing(false);
  };

  const [operatorOverride, setOperatorOverride] = useState<boolean | null>(null);

  const isOperatorEnabled = operatorOverride !== null ? operatorOverride : (operatorStatus ? Boolean(
    operatorStatus.cni?.enabled && 
    operatorStatus.coreDNS?.enabled &&
    operatorStatus.networkPolicy?.enabled &&
    operatorStatus.podConnectivity?.enabled
  ) : true);

  const handleToggleOperator = async (enable: boolean) => {
    setIsProcessing(true);
    setOperatorOverride(enable);
    try {
      const cmd = await k8sApi.toggleOperator(enable);
      onLog(cmd, 'system');
      if (enable) {
        onLog(`[Operator Master Switch] Operator Auto-Healing ENABLED. Pipeline active!`, 'success');
      } else {
        onLog(`[Operator Master Switch] Operator Auto-Healing DISABLED. Native K8s behavior mode!`, 'critical');
      }
    } catch (e: any) {
      setOperatorOverride(!enable);
      onLog(`Failed to toggle operator: ${e.message}`, 'error');
    }
    setIsProcessing(false);
  };

  return (
    <div className="control-panel glass-panel" style={{ overflowY: 'auto' }}>
      <div className="panel-header">
        <Terminal className="icon" size={24} color="#3b82f6" />
        <h2>All 12 Real Experiments</h2>
      </div>

      {/* MASTER OPERATOR TOGGLE SWITCH */}
      <div style={{
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'space-between',
        padding: '10px 12px',
        background: isOperatorEnabled ? 'rgba(16, 185, 129, 0.12)' : 'rgba(239, 68, 68, 0.12)',
        border: `1px solid ${isOperatorEnabled ? '#10b981' : '#ef4444'}`,
        borderRadius: '8px',
        marginBottom: '15px'
      }}>
        <div>
          <div style={{ fontWeight: 600, fontSize: '0.85rem', color: isOperatorEnabled ? '#10b981' : '#ef4444' }}>
            Operator Status: {isOperatorEnabled ? 'ENABLED' : 'DISABLED'}
          </div>
          <div style={{ fontSize: '0.7rem', color: '#aaa' }}>
            {isOperatorEnabled ? 'Auto-healing active' : 'Native K8s mode (No auto-healing)'}
          </div>
        </div>
        <button
          className={`btn ${isOperatorEnabled ? 'crash-btn' : 'heal-btn'}`}
          onClick={() => handleToggleOperator(!isOperatorEnabled)}
          style={{ width: 'auto', padding: '6px 12px', fontSize: '0.75rem' }}
          disabled={isProcessing}
        >
          {isOperatorEnabled ? 'Disable' : 'Enable'}
        </button>
      </div>

      <div style={{ display: 'flex', gap: '8px', marginBottom: '15px', flexDirection: 'column' }}>
        <input 
          type="text" 
          placeholder="App name (e.g. frontend)" 
          value={newAppName}
          onChange={(e) => setNewAppName(e.target.value)}
          className="app-input"
        />
        <button className="btn deploy-btn" onClick={handleDeploy} disabled={!newAppName || isProcessing}>
          <Plus size={18} />
          Deploy Standard App
        </button>
      </div>

      <div className="selected-pod-banner" style={{ borderLeft: '4px solid #3b82f6', display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <div>
          <strong>Target Selected:</strong> {selectedPod || 'None (Click a Pod!)'}
        </div>
        {selectedPod && (
          <button 
            className="btn heal-btn" 
            onClick={onOpenLogs}
            style={{ width: 'auto', padding: '4px 8px', fontSize: '0.75rem', margin: 0, display: 'flex', alignItems: 'center', gap: '4px' }}
          >
            <Terminal size={12} /> View Logs
          </button>
        )}
      </div>

      {/* EXPERIMENTS UI */}
      <div className="experiments-section" style={{ display: 'flex', flexDirection: 'column', gap: '15px', marginTop: '15px' }}>
        
        {/* MODULE 1: CNI */}
        <div className="experiment-card" style={{ padding: '10px', background: 'rgba(255,255,255,0.05)', borderRadius: '8px' }}>
          <h4><Network size={16} style={{display:'inline', marginRight: '6px'}} />1. CNI Subsystem (E1 - E3)</h4>
          <p style={{fontSize: '0.75rem', color: '#aaa', margin: '4px 0'}}>Calico agent crash, IPAM pool exhaustion, IPPool disabled.</p>
          <div style={{ display: 'flex', flexDirection: 'column', gap: '6px', marginTop: '8px' }}>
            <button className="btn crash-btn" onClick={exp1KillCNI} disabled={!selectedPodObj?.isCNI || isProcessing}>
              <ShieldAlert size={14} /> E1: Kill Calico Agent Pod
            </button>
            <div style={{ display: 'flex', gap: '6px' }}>
              <button className="btn crash-btn" onClick={exp2ExhaustIPAM} disabled={isProcessing} style={{flex: 1}}>
                E2: Fill IPAM Pool
              </button>
              <button className="btn heal-btn" onClick={exp2CleanupIPAM} disabled={isProcessing} style={{flex: 1}}>
                Clean IPAM
              </button>
            </div>
            <div style={{ display: 'flex', gap: '6px' }}>
              <button className="btn crash-btn" onClick={() => exp3ToggleIPPool(true)} disabled={isProcessing} style={{flex: 1}}>
                E3: Disable IPPool
              </button>
              <button className="btn heal-btn" onClick={() => exp3ToggleIPPool(false)} disabled={isProcessing} style={{flex: 1}}>
                Enable IPPool
              </button>
            </div>
          </div>
        </div>

        {/* MODULE 2: CoreDNS */}
        <div className="experiment-card" style={{ padding: '10px', background: 'rgba(255,255,255,0.05)', borderRadius: '8px' }}>
          <h4><Zap size={16} style={{display:'inline', marginRight: '6px'}} />2. CoreDNS Subsystem (E4 - E7)</h4>
          <p style={{fontSize: '0.75rem', color: '#aaa', margin: '4px 0'}}>Replicas to 0, CPU throttling latency, upstream & syntax corrupt.</p>
          <div style={{ display: 'flex', flexDirection: 'column', gap: '6px', marginTop: '8px' }}>
            <div style={{ display: 'flex', gap: '6px' }}>
              <button className="btn crash-btn" onClick={() => exp4ScaleCoreDNS(0)} disabled={isProcessing} style={{flex: 1}}>
                E4: Scale 0
              </button>
              <button className="btn heal-btn" onClick={() => exp4ScaleCoreDNS(2)} disabled={isProcessing} style={{flex: 1}}>
                Restore (2)
              </button>
            </div>
            <div style={{ display: 'flex', gap: '6px' }}>
              <button className="btn crash-btn" onClick={() => exp5ThrottleCPU(true)} disabled={isProcessing} style={{flex: 1}}>
                E5: CPU Limit (5m)
              </button>
              <button className="btn heal-btn" onClick={() => exp5ThrottleCPU(false)} disabled={isProcessing} style={{flex: 1}}>
                Restore CPU
              </button>
            </div>
            <div style={{ display: 'flex', gap: '6px' }}>
              <button className="btn crash-btn" onClick={() => exp6CorruptUpstream(true)} disabled={isProcessing} style={{flex: 1}}>
                E6: Bad Upstream
              </button>
              <button className="btn heal-btn" onClick={() => exp6CorruptUpstream(false)} disabled={isProcessing} style={{flex: 1}}>
                Fix Corefile
              </button>
            </div>
            <div style={{ display: 'flex', gap: '6px' }}>
              <button className="btn crash-btn" onClick={() => exp7CrashLoop(true)} disabled={isProcessing} style={{flex: 1}}>
                E7: Bad Plugin
              </button>
              <button className="btn heal-btn" onClick={() => exp7CrashLoop(false)} disabled={isProcessing} style={{flex: 1}}>
                Fix Corefile
              </button>
            </div>
          </div>
        </div>

        {/* MODULE 3: NetworkPolicy */}
        <div className="experiment-card" style={{ padding: '10px', background: 'rgba(255,255,255,0.05)', borderRadius: '8px' }}>
          <h4><FileCode size={16} style={{display:'inline', marginRight: '6px'}} />3. NetworkPolicy Drift (E8)</h4>
          <p style={{fontSize: '0.75rem', color: '#aaa', margin: '4px 0'}}>Test silent policy drift when Felix crashes.</p>
          <div style={{ display: 'flex', gap: '6px', marginTop: '8px' }}>
            <button className="btn crash-btn" onClick={exp8FelixDrift} disabled={!selectedPodObj || selectedPodObj.isCNI || selectedPodObj.isCoreDNS || isProcessing} style={{flex: 1}}>
              E8: Deny-All Policy
            </button>
            <button className="btn heal-btn" onClick={exp8RemovePolicy} disabled={isProcessing} style={{flex: 1}}>
              Remove Policy
            </button>
          </div>
        </div>

        {/* MODULE 4: Pod Connectivity */}
        <div className="experiment-card" style={{ padding: '10px', background: 'rgba(255,255,255,0.05)', borderRadius: '8px' }}>
          <h4><WifiOff size={16} style={{display:'inline', marginRight: '6px'}} />4. Pod Connectivity (E9 - E12)</h4>
          <p style={{fontSize: '0.75rem', color: '#aaa', margin: '4px 0'}}>veth down, tunl0 down, iptables FORWARD drop, inter-node break.</p>
          <div style={{ display: 'flex', flexDirection: 'column', gap: '6px', marginTop: '8px' }}>
            <button className="btn crash-btn" onClick={exp9DownVeth} disabled={!selectedPodObj || isProcessing}>
              E9: Down Local veth
            </button>
            <div style={{ display: 'flex', gap: '6px' }}>
              <button className="btn crash-btn" onClick={() => exp10ToggleTunnel(true)} disabled={!selectedPodObj || isProcessing} style={{flex: 1}}>
                E10: Down tunl0
              </button>
              <button className="btn heal-btn" onClick={() => exp10ToggleTunnel(false)} disabled={!selectedPodObj || isProcessing} style={{flex: 1}}>
                Up tunl0
              </button>
            </div>
            <div style={{ display: 'flex', gap: '6px' }}>
              <button className="btn crash-btn" onClick={() => exp11ToggleIptables(true)} disabled={!selectedPodObj || isProcessing} style={{flex: 1}}>
                E11: Drop iptables
              </button>
              <button className="btn heal-btn" onClick={() => exp11ToggleIptables(false)} disabled={!selectedPodObj || isProcessing} style={{flex: 1}}>
                Restore iptables
              </button>
            </div>
            <div style={{ display: 'flex', gap: '6px' }}>
              <button className="btn crash-btn" onClick={() => exp12ToggleInterNode(true)} disabled={!selectedPodObj || isProcessing} style={{flex: 1}}>
                E12: Break Inter-Node
              </button>
              <button className="btn heal-btn" onClick={() => exp12ToggleInterNode(false)} disabled={!selectedPodObj || isProcessing} style={{flex: 1}}>
                Restore Route
              </button>
            </div>
          </div>
        </div>
        
        {/* Verification Tools */}
        <div className="experiment-card" style={{ padding: '10px', background: 'rgba(255,255,255,0.05)', borderRadius: '8px' }}>
          <h4>Verification Tools</h4>
          <div style={{ display: 'flex', gap: '8px', marginTop: '6px' }}>
            <button className="btn" onClick={runPingTest} disabled={!selectedPodObj || isProcessing} style={{ background: '#4b5563', color: 'white', flex: 1 }}>
              Run Ping Test
            </button>
            <button className="btn" onClick={handleDelete} disabled={!selectedPodObj || selectedPodObj.isCNI || selectedPodObj.isCoreDNS || isProcessing} style={{ background: '#ef4444', color: 'white', flex: 1 }}>
              <Trash2 size={16} /> Delete App
            </button>
          </div>
        </div>

      </div>

      {/* OPERATOR STATUS */}
      <div className="status-box" style={{ marginTop: '20px' }}>
        <h3>Operator Remediation Status</h3>
        {!operatorStatus ? (
          <div style={{ fontSize: '0.85rem', color: '#888' }}>Waiting for CRD (networkremediation-sample)...</div>
        ) : (
          <div style={{ display: 'flex', flexDirection: 'column', gap: '8px', marginTop: '10px' }}>
             <StatusRow name="CNI Module" status={operatorStatus.cni} />
             <StatusRow name="CoreDNS Module" status={operatorStatus.coreDNS} />
             <StatusRow name="NetPolicy Module" status={operatorStatus.networkPolicy} />
             <StatusRow name="Connectivity Module" status={operatorStatus.podConnectivity} />
          </div>
        )}
      </div>
    </div>
  );
}

function StatusRow({ name, status }: { name: string, status: { healthy: boolean, enabled: boolean, message?: string } }) {
  if (!status?.enabled) return <div style={{ fontSize: '0.85rem', color: '#666', display: 'flex', alignItems: 'center' }}><span style={{width: '16px'}}></span> {name}: Disabled</div>;
  
  return (
    <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', fontSize: '0.85rem' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: '6px' }}>
        {status.healthy ? <CheckCircle size={14} color="#10b981" /> : <XCircle size={14} color="#ef4444" className="pulse-red" />}
        <span style={{ color: status.healthy ? '#d1d5db' : '#ef4444' }}>{name}</span>
      </div>
      <div style={{ fontSize: '0.75rem', color: '#888', maxWidth: '100px', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
        {status.message || (status.healthy ? 'Healthy' : 'Degraded')}
      </div>
    </div>
  );
}
