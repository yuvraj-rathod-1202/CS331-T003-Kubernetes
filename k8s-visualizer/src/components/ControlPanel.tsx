import { useState, useEffect } from 'react';
import { ShieldAlert, Trash2, Terminal, CheckCircle, XCircle, Zap, Network, WifiOff, FileCode } from 'lucide-react';
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

export function ControlPanel({ onLog, selectedPod, k8sPods, operatorStatus, k8sApi, onOpenLogs }: ControlPanelProps) {
  const [iptablesDropped, setIptablesDropped] = useState<boolean>(false);
  const [vethInfo, setVethInfo] = useState<{ iface: string; isDown: boolean } | null>(null);
  const [tunl0Down, setTunl0Down] = useState<boolean>(false);
  const [isProcessing, setIsProcessing] = useState(false);

  const selectedPodObj = k8sPods.find(p => p.name === selectedPod);

  // Real-time Calico host veth interface polling for the selected pod
  useEffect(() => {
    if (!selectedPodObj?.nodeName || !selectedPodObj?.name || !k8sApi.getVethStatus) {
      setVethInfo(null);
      return;
    }
    let isMounted = true;
    const checkVeth = async () => {
      try {
        const res = await k8sApi.getVethStatus(
          selectedPodObj.nodeName,
          selectedPodObj.name,
          selectedPodObj.namespace,
          selectedPodObj.podIP
        );
        if (isMounted && res.iface) {
          setVethInfo({ iface: res.iface, isDown: !!res.isDown });
        }
      } catch {
        // ignore polling errors
      }
    };
    checkVeth();
    const interval = setInterval(checkVeth, 3000);
    return () => {
      isMounted = false;
      clearInterval(interval);
    };
  }, [selectedPodObj?.name, selectedPodObj?.nodeName, selectedPodObj?.podIP, k8sApi]);

  // Real-time tunl0 status polling for the selected pod's node
  useEffect(() => {
    if (!selectedPodObj?.nodeName || !k8sApi.getTunnelStatus) {
      setTunl0Down(false);
      return;
    }
    let isMounted = true;
    const checkTunl = async () => {
      try {
        const res = await k8sApi.getTunnelStatus(selectedPodObj.nodeName);
        if (isMounted && typeof res.isDown === 'boolean') {
          setTunl0Down(res.isDown);
        }
      } catch {
        // ignore polling errors
      }
    };
    checkTunl();
    const interval = setInterval(checkTunl, 3000);
    return () => {
      isMounted = false;
      clearInterval(interval);
    };
  }, [selectedPodObj?.nodeName, k8sApi]);


  const handleDeletePod = async () => {
    if (selectedPodObj && !selectedPodObj.isCNI && !selectedPodObj.isCoreDNS) {
      setIsProcessing(true);
      try {
        const cmd = await k8sApi.deletePod(selectedPodObj.namespace, selectedPodObj.name);
        onLog(cmd, 'system');
        onLog(`[Pod Deleted] Deleted pod ${selectedPodObj.name}.`, 'warning');
      } catch (e: any) {
        onLog(`Failed to delete pod: ${e.message}`, 'error');
      }
      setIsProcessing(false);
    }
  };

  // --- MODULE 1: CNI Subsystem (E1 - E3) ---
  const exp1KillCNI = async () => {
    if (!selectedPodObj || !selectedPodObj.isCNI) return;
    setIsProcessing(true);
    try {
      const cmd = await k8sApi.deletePod(selectedPodObj.namespace, selectedPodObj.name);
      onLog(cmd, 'system');
      onLog(`[CNI Crash] Killed CNI agent on node ${selectedPodObj.nodeName}. Watch for BGP route loss!`, 'warning');
    } catch (e: any) {
      onLog(`Failed: ${e.message}`, 'error');
    }
    setIsProcessing(false);
  };

  const exp2ExhaustIPAM = async () => {
    setIsProcessing(true);
    try {
      onLog(`1. Disabling default-ipv4-ippool...`, 'system');
      await k8sApi.setIPPoolDisabled(true);

      onLog(`2. Creating tiny /28 IPPool (14 usable IPs)...`, 'system');
      await k8sApi.createTinyIPPool();

      onLog(`3. Launching 20 replicas of ipam-filler...`, 'system');
      await k8sApi.deployCustomApp('ipam-filler', undefined, 20);

      onLog(`[IPAM Exhaustion] Pool exhausted! Surplus pods are now stuck in ContainerCreating with 'No IPs available in pools'.`, 'critical');
    } catch (e: any) {
      onLog(`Failed: ${e.message}`, 'error');
    }
    setIsProcessing(false);
  };

  const exp2CleanupIPAM = async () => {
    setIsProcessing(true);
    try {
      onLog(`1. Deleting deployment ipam-filler...`, 'system');
      await k8sApi.deleteCustomApp('ipam-filler');

      onLog(`2. Deleting tiny IPPool...`, 'system');
      await k8sApi.deleteTinyIPPool();

      onLog(`3. Re-enabling default-ipv4-ippool...`, 'system');
      await k8sApi.setIPPoolDisabled(false);

      onLog(`[IPAM Cleanup] Restored default IPPool and cleaned up filler pods.`, 'success');
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
        onLog(`[IPPool Disabled] Patched IPPool spec.disabled = true. New IP allocations will fail!`, 'critical');
      } else {
        onLog(`[IPPool Enabled] Re-enabled IPPool spec.disabled = false.`, 'success');
      }
    } catch (e: any) {
      onLog(`Failed to patch IPPool: ${e.message}`, 'error');
    }
    setIsProcessing(false);
  };

  // --- MODULE 2: CoreDNS Subsystem ---
  const exp4ScaleCoreDNS = async (replicas: number) => {
    setIsProcessing(true);
    try {
      const cmd = await k8sApi.scaleDeployment('kube-system', 'coredns', replicas);
      onLog(cmd, 'system');
      if (replicas === 0) {
        onLog(`[CoreDNS Scale 0] CoreDNS scaled to 0. All cluster DNS resolution will fail!`, 'critical');
      } else {
        onLog(`[CoreDNS Scale Restore] Restored CoreDNS to ${replicas} replicas.`, 'success');
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
        onLog(`[CPU Throttle] CoreDNS CPU limit set to 5m. Query latency will spike >1000ms!`, 'warning');
      } else {
        onLog(`[CPU Throttle] CoreDNS CPU limits restored.`, 'success');
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
        onLog(`[Upstream Corrupt] Forward target set to 192.0.2.1. External DNS resolution will hang!`, 'critical');
      } else {
        onLog(`[Upstream Restored] Restored valid Corefile.`, 'success');
      }
    } catch (e: any) {
      onLog(`Failed: ${e.message}`, 'error');
    }
    setIsProcessing(false);
  };

  // --- MODULE 3: NetworkPolicy Enforcement ---
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
      onLog(`[Felix Drift] Applied Deny-All policy to ${selectedPodObj.appLabel}. Now crash Felix to observe policy drift!`, 'critical');
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
        onLog(`[Felix Drift] Removed Deny-All NetworkPolicy.`, 'success');
      }
    } catch (e: any) {
      onLog(`Note: Policy already removed.`, 'info');
    }
    setIsProcessing(false);
  };

  // --- MODULE 4: Pod-to-Pod Connectivity ---
  const exp9ToggleVeth = async (down: boolean) => {
    if (!selectedPodObj || !selectedPodObj.nodeName) return;
    setIsProcessing(true);
    try {
      const action = down ? 'down' : 'up';
      const res = await k8sApi.toggleVeth(
        selectedPodObj.nodeName,
        selectedPodObj.name,
        selectedPodObj.namespace,
        action,
        selectedPodObj.podIP,
        vethInfo?.iface
      );
      if (res.iface) {
        setVethInfo({ iface: res.iface, isDown: down });
      }
      onLog(`docker exec ${selectedPodObj.nodeName} sudo ip link set ${res.iface || vethInfo?.iface || 'cali...'} ${action}`, 'system');
      if (down) {
        onLog(`[Local veth Down] Downed interface ${res.iface || 'veth'} for pod ${selectedPodObj.name} on ${selectedPodObj.nodeName}. Local traffic will drop!`, 'critical');
      } else {
        onLog(`[Local veth Restored] Brought interface ${res.iface || 'veth'} back UP on ${selectedPodObj.nodeName}.`, 'success');
      }
    } catch (e: any) {
      onLog(`Failed: ${e.message}`, 'error');
    }
    setIsProcessing(false);
  };

  const exp10ToggleTunnel = async (down: boolean) => {
    if (!selectedPodObj || !selectedPodObj.nodeName) return;
    setIsProcessing(true);
    try {
      const action = down ? 'down' : 'up';
      onLog(`docker exec ${selectedPodObj.nodeName} sudo ip link set tunl0 ${action}`, 'system');
      const res = await k8sApi.toggleTunnel(selectedPodObj.nodeName, action);
      setTunl0Down(down);
      if (down) {
        onLog(`[Overlay Tunnel Down] Downed tunl0 interface on ${selectedPodObj.nodeName}. Cross-node pod traffic will drop!`, 'critical');
      } else {
        onLog(`[Overlay Tunnel Restored] Brought tunl0 interface back UP on ${selectedPodObj.nodeName}.`, 'success');
      }
    } catch (e: any) {
      onLog(`Failed: ${e.message}`, 'error');
    }
    setIsProcessing(false);
  };

  // Utility Ping Test
  const runPingTest = async () => {
    if (!selectedPodObj) {
      onLog('Please select a pod from the cluster map to run ping test.', 'warning');
      return;
    }
    setIsProcessing(true);
    try {
      onLog(`kubectl exec -it ${selectedPodObj.name} -n ${selectedPodObj.namespace} -- ping -c 3 8.8.8.8`, 'system');
      onLog(`Executing direct ping test from ${selectedPodObj.name}...`, 'info');
      const res = await k8sApi.runDirectPing(selectedPodObj.namespace, selectedPodObj.name);
      if (res.packetLossPercent === 100) {
        onLog(`[Ping DROPPED] 100% packet loss! Traffic blocked by iptables or broken network interface.\n${res.output}`, 'critical');
      } else if (res.packetLossPercent > 0) {
        onLog(`[Ping DEGRADED] ${res.packetLossPercent}% packet loss.\n${res.output}`, 'warning');
      } else {
        onLog(`[Ping SUCCESS] 0% packet loss. Network connectivity is healthy!\n${res.output}`, 'success');
      }
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
          <h4><Network size={16} style={{display:'inline', marginRight: '6px'}} />CNI Subsystem</h4>
          <p style={{fontSize: '0.75rem', color: '#aaa', margin: '4px 0'}}>Calico agent crash, IPAM pool exhaustion, IPPool disabled.</p>
          <div style={{ display: 'flex', flexDirection: 'column', gap: '6px', marginTop: '8px' }}>
            <button className="btn crash-btn" onClick={exp1KillCNI} disabled={!selectedPodObj?.isCNI || isProcessing}>
              <ShieldAlert size={14} /> Kill Calico Agent Pod
            </button>
            <div style={{ display: 'flex', gap: '6px' }}>
              <button className="btn crash-btn" onClick={exp2ExhaustIPAM} disabled={isProcessing} style={{flex: 1}}>
                Fill IPAM Pool
              </button>
              <button className="btn heal-btn" onClick={exp2CleanupIPAM} disabled={isProcessing} style={{flex: 1}}>
                Clean IPAM
              </button>
            </div>
            <div style={{ display: 'flex', gap: '6px' }}>
              <button className="btn crash-btn" onClick={() => exp3ToggleIPPool(true)} disabled={isProcessing} style={{flex: 1}}>
                Disable IPPool
              </button>
              <button className="btn heal-btn" onClick={() => exp3ToggleIPPool(false)} disabled={isProcessing} style={{flex: 1}}>
                Enable IPPool
              </button>
            </div>
          </div>
        </div>

        {/* MODULE 2: CoreDNS */}
        <div className="experiment-card" style={{ padding: '10px', background: 'rgba(255,255,255,0.05)', borderRadius: '8px' }}>
          <h4><Zap size={16} style={{display:'inline', marginRight: '6px'}} />CoreDNS Subsystem</h4>
          <p style={{fontSize: '0.75rem', color: '#aaa', margin: '4px 0'}}>Replicas to 0, CPU throttling latency, upstream corrupt.</p>
          <div style={{ display: 'flex', flexDirection: 'column', gap: '6px', marginTop: '8px' }}>
            <div style={{ display: 'flex', gap: '6px' }}>
              <button className="btn crash-btn" onClick={() => exp4ScaleCoreDNS(0)} disabled={isProcessing} style={{flex: 1}}>
                Scale to 0
              </button>
              <button className="btn heal-btn" onClick={() => exp4ScaleCoreDNS(2)} disabled={isProcessing} style={{flex: 1}}>
                Restore (2)
              </button>
            </div>
            <div style={{ display: 'flex', gap: '6px' }}>
              <button className="btn crash-btn" onClick={() => exp5ThrottleCPU(true)} disabled={isProcessing} style={{flex: 1}}>
                CPU Limit (5m)
              </button>
              <button className="btn heal-btn" onClick={() => exp5ThrottleCPU(false)} disabled={isProcessing} style={{flex: 1}}>
                Restore CPU
              </button>
            </div>
            <div style={{ display: 'flex', gap: '6px' }}>
              <button className="btn crash-btn" onClick={() => exp6CorruptUpstream(true)} disabled={isProcessing} style={{flex: 1}}>
                Corrupt Upstream
              </button>
              <button className="btn heal-btn" onClick={() => exp6CorruptUpstream(false)} disabled={isProcessing} style={{flex: 1}}>
                Fix Corefile
              </button>
            </div>
          </div>
        </div>

        {/* MODULE 3: NetworkPolicy */}
        {/* <div className="experiment-card" style={{ padding: '10px', background: 'rgba(255,255,255,0.05)', borderRadius: '8px' }}>
          <h4><FileCode size={16} style={{display:'inline', marginRight: '6px'}} />3. NetworkPolicy Drift</h4>
          <p style={{fontSize: '0.75rem', color: '#aaa', margin: '4px 0'}}>Test silent policy drift when Felix crashes.</p>
          <div style={{ display: 'flex', gap: '6px', marginTop: '8px' }}>
            <button className="btn crash-btn" onClick={exp8FelixDrift} disabled={!selectedPodObj || selectedPodObj.isCNI || selectedPodObj.isCoreDNS || isProcessing} style={{flex: 1}}>
              Deny-All Policy
            </button>
            <button className="btn heal-btn" onClick={exp8RemovePolicy} disabled={isProcessing} style={{flex: 1}}>
              Remove Policy
            </button>
          </div>
        </div> */}

        {/* MODULE 4: Pod Connectivity */}
        <div className="experiment-card" style={{ padding: '10px', background: 'rgba(255,255,255,0.05)', borderRadius: '8px' }}>
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
            <h4><WifiOff size={16} style={{display:'inline', marginRight: '6px'}} />Pod Connectivity</h4>
            <div style={{ display: 'flex', gap: '4px' }}>
              {selectedPodObj && vethInfo?.isDown && (
                <span style={{ 
                  padding: '2px 6px', 
                  borderRadius: '4px', 
                  fontWeight: 600, 
                  fontSize: '0.7rem',
                  background: 'rgba(239, 68, 68, 0.2)', 
                  color: '#ef4444',
                  border: '1px solid #ef4444'
                }}>
                  veth: DOWN {vethInfo?.iface ? `(${vethInfo.iface})` : ''} ⚠️
                </span>
              )}
              {selectedPodObj && tunl0Down && (
                <span style={{ 
                  padding: '2px 6px', 
                  borderRadius: '4px', 
                  fontWeight: 600, 
                  fontSize: '0.7rem',
                  background: 'rgba(239, 68, 68, 0.2)', 
                  color: '#ef4444',
                  border: '1px solid #ef4444'
                }}>
                  tunl0: DOWN ({selectedPodObj.nodeName}) ⚠️
                </span>
              )}
            </div>
          </div>
          <p style={{fontSize: '0.75rem', color: '#aaa', margin: '4px 0'}}>Intra-node (veth down) and Inter-node (tunl0 down) failure experiments.</p>

          <div style={{ display: 'flex', flexDirection: 'column', gap: '6px', marginTop: '8px' }}>
            <div style={{ display: 'flex', gap: '6px' }}>
              <button 
                className="btn crash-btn" 
                onClick={() => exp9ToggleVeth(true)} 
                disabled={!selectedPodObj || isProcessing}
                style={{ flex: 1 }}
                title={vethInfo?.iface ? `Bring down host veth ${vethInfo.iface}` : 'Select a pod to down its veth'}
              >
                Down veth
              </button>
              <button 
                className="btn heal-btn" 
                onClick={() => exp9ToggleVeth(false)} 
                disabled={!selectedPodObj || isProcessing}
                style={{ flex: 1 }}
                title={vethInfo?.iface ? `Bring up host veth ${vethInfo.iface}` : 'Select a pod to up its veth'}
              >
                Up veth
              </button>
            </div>
            <div style={{ display: 'flex', gap: '6px' }}>
              <button 
                className="btn crash-btn" 
                onClick={() => exp10ToggleTunnel(true)} 
                disabled={!selectedPodObj || isProcessing} 
                style={{ flex: 1 }}
                title={selectedPodObj?.nodeName ? `Bring down tunl0 on ${selectedPodObj.nodeName}` : 'Select a pod to target its node'}
              >
                Down tunl0 
              </button>
              <button 
                className="btn heal-btn" 
                onClick={() => exp10ToggleTunnel(false)} 
                disabled={!selectedPodObj || isProcessing} 
                style={{ flex: 1 }}
                title={selectedPodObj?.nodeName ? `Bring up tunl0 on ${selectedPodObj.nodeName}` : 'Select a pod to target its node'}
              >
                Up tunl0
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
            <button className="btn" onClick={handleDeletePod} disabled={!selectedPodObj || selectedPodObj.isCNI || selectedPodObj.isCoreDNS || isProcessing} style={{ background: '#ef4444', color: 'white', flex: 1 }}>
              <Trash2 size={16} /> Delete Pod
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
