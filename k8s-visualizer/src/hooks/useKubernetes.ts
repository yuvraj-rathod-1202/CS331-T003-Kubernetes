import { useState, useEffect, useCallback } from 'react';

export interface K8sNode {
  name: string;
  status: string;
}

export interface K8sPod {
  name: string;
  namespace: string;
  nodeName: string;
  status: string;
  appLabel: string;
  isCNI: boolean;
  isCoreDNS: boolean;
}

export interface OperatorStatus {
  cni: { healthy: boolean, enabled: boolean, message?: string };
  coreDNS: { healthy: boolean, enabled: boolean, message?: string };
  networkPolicy: { healthy: boolean, enabled: boolean, message?: string };
  podConnectivity: { healthy: boolean, enabled: boolean, message?: string };
}

export function useKubernetes() {
  const [nodes, setNodes] = useState<K8sNode[]>([]);
  const [pods, setPods] = useState<K8sPod[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [operatorStatus, setOperatorStatus] = useState<OperatorStatus | null>(null);

  const fetchClusterState = useCallback(async () => {
    try {
      const nodesRes = await fetch('/api/v1/nodes');
      if (!nodesRes.ok) throw new Error('Failed to fetch nodes');
      const nodesData = await nodesRes.json();
      
      const parsedNodes: K8sNode[] = nodesData.items.map((n: any) => ({
        name: n.metadata.name,
        status: n.status.conditions.find((c: any) => c.type === 'Ready')?.status === 'True' ? 'Ready' : 'NotReady',
      }));

      const podsRes = await fetch('/api/v1/pods');
      if (!podsRes.ok) throw new Error('Failed to fetch pods');
      const podsData = await podsRes.json();

      const parsedPods: K8sPod[] = podsData.items
        .filter((p: any) => {
          const ns = p.metadata.namespace;
          const name = p.metadata.name;
          const isDefault = ns === 'default';
          const isSystemCNI = (ns === 'kube-system' || ns === 'calico-system') && 
                              (name.includes('calico-node') || name.includes('kindnet') || name.includes('flannel'));
          const isCoreDNS = ns === 'kube-system' && name.includes('coredns');
          return isDefault || isSystemCNI || isCoreDNS;
        })
        .map((p: any) => {
          let status = p.status.phase || 'Unknown';
          if (p.metadata.deletionTimestamp) {
            status = 'Terminating';
          } else if (
            p.status.conditions?.some((c: any) => c.type === 'Ready' && c.status === 'False') ||
            (p.status.phase === 'Running' && !p.status.containerStatuses?.every((cs: any) => cs.ready))
          ) {
            status = 'NotReady';
          }
          
          let appLabel = p.metadata.labels?.app || p.metadata.labels?.run || p.metadata.labels?.['k8s-app'];
          if (!appLabel) {
            const parts = p.metadata.name.split('-');
            if (parts.length >= 3) {
              appLabel = parts.slice(0, -2).join('-');
            } else {
              appLabel = p.metadata.name;
            }
          }
          
          return {
            name: p.metadata.name,
            namespace: p.metadata.namespace,
            nodeName: p.spec.nodeName || 'unassigned',
            status: status,
            appLabel: appLabel,
            isCNI: (p.metadata.namespace === 'kube-system' || p.metadata.namespace === 'calico-system') && p.metadata.name.includes('calico-node'),
            isCoreDNS: p.metadata.namespace === 'kube-system' && p.metadata.name.includes('coredns'),
          };
        });

      setNodes(parsedNodes);
      setPods(parsedPods);

      // Fetch Operator Status — try both CRD names (cluster-network-remediation is the real one)
      try {
        const CRD_NAMES = ['cluster-network-remediation', 'networkremediation-sample'];
        let crdData: any = null;
        for (const crdName of CRD_NAMES) {
          const crdRes = await fetch(`/apis/remediation.cn-operator.yuvraj-rathod-1202.github.io/v1alpha1/networkremediations/${crdName}`);
          if (crdRes.ok) {
            crdData = await crdRes.json();
            break;
          }
        }
        if (crdData) {
          const spec = crdData.spec || {};
          const st = crdData.status || {};
          setOperatorStatus({
            cni: {
              healthy: st.cni?.healthy ?? true,
              enabled: spec.cni?.enabled ?? true,
              message: st.cni?.message
            },
            coreDNS: {
              healthy: st.coreDNS?.healthy ?? true,
              enabled: spec.coreDNS?.enabled ?? true,
              message: st.coreDNS?.message
            },
            networkPolicy: {
              healthy: st.networkPolicy?.healthy ?? true,
              enabled: spec.networkPolicy?.enabled ?? true,
              message: st.networkPolicy?.message
            },
            podConnectivity: {
              healthy: st.podConnectivity?.healthy ?? true,
              enabled: spec.podConnectivity?.enabled ?? true,
              message: st.podConnectivity?.message
            }
          });
        }
      } catch (crdErr) {
        console.warn("Operator CRD not found or reachable", crdErr);
      }

      setError(null);
    } catch (err: any) {
      console.error(err);
      setError('Could not connect to Kubernetes API. Make sure kubectl proxy is running.');
    }
  }, []);

  useEffect(() => {
    fetchClusterState();
    const interval = setInterval(fetchClusterState, 1000);
    return () => clearInterval(interval);
  }, [fetchClusterState]);

  // K8s API Wrappers for Experiments
  const deletePod = async (namespace: string, name: string) => {
    const res = await fetch(`/api/v1/namespaces/${namespace}/pods/${name}`, { method: 'DELETE' });
    if (!res.ok) throw new Error(await res.text());
    return `kubectl delete pod ${name} -n ${namespace}`;
  };

  const scaleDeployment = async (namespace: string, name: string, replicas: number) => {
    const patch = [{ op: 'replace', path: '/spec/replicas', value: replicas }];
    const res = await fetch(`/apis/apps/v1/namespaces/${namespace}/deployments/${name}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json-patch+json' },
      body: JSON.stringify(patch)
    });
    if (!res.ok) throw new Error(await res.text());
    return `kubectl scale deployment ${name} -n ${namespace} --replicas=${replicas}`;
  };

  const applyNetworkPolicy = async (namespace: string, name: string, policy: any) => {
    const check = await fetch(`/apis/networking.k8s.io/v1/namespaces/${namespace}/networkpolicies/${name}`);
    if (check.ok) {
       const res = await fetch(`/apis/networking.k8s.io/v1/namespaces/${namespace}/networkpolicies/${name}`, {
         method: 'PUT',
         headers: { 'Content-Type': 'application/json' },
         body: JSON.stringify(policy)
       });
       if (!res.ok) throw new Error(await res.text());
    } else {
       const res = await fetch(`/apis/networking.k8s.io/v1/namespaces/${namespace}/networkpolicies`, {
         method: 'POST',
         headers: { 'Content-Type': 'application/json' },
         body: JSON.stringify(policy)
       });
       if (!res.ok) throw new Error(await res.text());
    }
    return `kubectl apply -f [network-policy-manifest]`;
  };

  const deleteNetworkPolicy = async (namespace: string, name: string) => {
    const res = await fetch(`/apis/networking.k8s.io/v1/namespaces/${namespace}/networkpolicies/${name}`, { method: 'DELETE' });
    if (!res.ok && res.status !== 404) throw new Error(await res.text());
    return `kubectl delete networkpolicy ${name} -n ${namespace}`;
  };

  const toggleOperator = async (enabled: boolean) => {
    const patch = {
      spec: {
        cni: { enabled },
        coreDNS: { enabled },
        networkPolicy: { enabled },
        podConnectivity: { enabled },
      }
    };
    // Patch BOTH CRD instances so whichever the operator watches gets updated
    const CRD_NAMES = ['cluster-network-remediation', 'networkremediation-sample'];
    let lastError: string | null = null;
    let patchedAtLeastOne = false;
    for (const crdName of CRD_NAMES) {
      const res = await fetch(`/apis/remediation.cn-operator.yuvraj-rathod-1202.github.io/v1alpha1/networkremediations/${crdName}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/merge-patch+json' },
        body: JSON.stringify(patch)
      });
      if (res.ok) {
        patchedAtLeastOne = true;
      } else {
        lastError = await res.text();
      }
    }
    if (!patchedAtLeastOne) throw new Error(lastError || 'Failed to patch any NetworkRemediation CRD');
    await fetchClusterState();
    return `kubectl patch networkremediation cluster-network-remediation --type=merge -p '{"spec":{"cni":{"enabled":${enabled}},"coreDNS":{"enabled":${enabled}},"networkPolicy":{"enabled":${enabled}},"podConnectivity":{"enabled":${enabled}}}}'`;
  };

  const setIPPoolDisabled = async (disabled: boolean) => {
    // Try patching via Calico CRD endpoint
    let res = await fetch(`/apis/crd.projectcalico.org/v1/ippools/default-ipv4-ippool`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/merge-patch+json' },
      body: JSON.stringify({ spec: { disabled } })
    });
    if (!res.ok) {
      // Fallback to projectcalico.org/v3
      res = await fetch(`/apis/projectcalico.org/v3/ippools/default-ipv4-ippool`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/merge-patch+json' },
        body: JSON.stringify({ spec: { disabled } })
      });
    }
    if (!res.ok) throw new Error(await res.text());
    return `kubectl patch ippool default-ipv4-ippool --type=merge -p '{"spec":{"disabled":${disabled}}}'`;
  };

  const createTinyIPPool = async () => {
    const tinyPool = {
      apiVersion: 'crd.projectcalico.org/v1',
      kind: 'IPPool',
      metadata: { name: 'experiment-tiny-pool' },
      spec: {
        cidr: '10.200.0.0/28',
        blockSize: 28,
        ipipMode: 'Always',
        natOutgoing: true,
        nodeSelector: 'all()'
      }
    };
    const res = await fetch('/apis/crd.projectcalico.org/v1/ippools', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(tinyPool)
    });
    if (!res.ok && res.status !== 409) {
      throw new Error(await res.text());
    }
    return `kubectl apply -f experiments/manifests/tiny-ippool.yaml`;
  };

  const deleteTinyIPPool = async () => {
    const res = await fetch('/apis/crd.projectcalico.org/v1/ippools/experiment-tiny-pool', {
      method: 'DELETE'
    });
    if (!res.ok && res.status !== 404) {
      throw new Error(await res.text());
    }
    return `kubectl delete ippool experiment-tiny-pool`;
  };

  const patchDeploymentResources = async (namespace: string, name: string, cpuLimit: string | null) => {
    const patch = cpuLimit ? [
      {
        op: 'add',
        path: '/spec/template/spec/containers/0/resources',
        value: { limits: { cpu: cpuLimit }, requests: { cpu: '5m' } }
      }
    ] : [
      {
        op: 'remove',
        path: '/spec/template/spec/containers/0/resources/limits'
      }
    ];

    const res = await fetch(`/apis/apps/v1/namespaces/${namespace}/deployments/${name}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json-patch+json' },
      body: JSON.stringify(patch)
    });
    if (!res.ok) throw new Error(await res.text());
    return cpuLimit 
      ? `kubectl set resources deployment ${name} -n ${namespace} --limits=cpu=${cpuLimit}`
      : `kubectl set resources deployment ${name} -n ${namespace} --limits=cpu=""`;
  };

  const updateCoreDNSConfigMap = async (corefileContent: string) => {
    const cmRes = await fetch(`/api/v1/namespaces/kube-system/configmaps/coredns`);
    if (!cmRes.ok) throw new Error('Failed to fetch coredns ConfigMap');
    const cmData = await cmRes.json();
    
    cmData.data = { ...cmData.data, Corefile: corefileContent };

    const res = await fetch(`/api/v1/namespaces/kube-system/configmaps/coredns`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(cmData)
    });
    if (!res.ok) throw new Error(await res.text());
    return `kubectl edit configmap coredns -n kube-system [Corefile Updated]`;
  };

  const runNodeShellCommand = async (nodeName: string, command: string) => {
    const podName = `node-shell-${Date.now()}`;
    const podManifest = {
      apiVersion: "v1",
      kind: "Pod",
      metadata: { name: podName, namespace: "default" },
      spec: {
        hostNetwork: true,
        hostPID: true,
        nodeName: nodeName,
        containers: [{
          name: "shell",
          image: "alpine:3.18",
          command: ["/bin/sh", "-c", `nsenter -t 1 -m -u -i -n -p -- ${command} && sleep 5`],
          securityContext: { privileged: true }
        }],
        restartPolicy: "Never",
        tolerations: [{ operator: "Exists" }]
      }
    };

    const res = await fetch('/api/v1/namespaces/default/pods', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(podManifest)
    });
    
    if (!res.ok) throw new Error(await res.text());
    
    setTimeout(() => {
      fetch(`/api/v1/namespaces/default/pods/${podName}`, { method: 'DELETE' });
    }, 8000);

    return `kubectl run ${podName} --overrides='{"spec":{"hostNetwork":true,"hostPID":true,"nodeName":"${nodeName}"}}' --privileged -- nsenter -t 1 -m -u -i -n -p -- ${command}`;
  };

  const runCommandAndGetLogs = async (command: string, podLabel: string = "ping-job") => {
    const jobName = `job-${Date.now()}`;
    const jobManifest = {
      apiVersion: "batch/v1",
      kind: "Job",
      metadata: { name: jobName, namespace: "default" },
      spec: {
        template: {
          metadata: { labels: { app: podLabel } },
          spec: {
            containers: [{
              name: "cmd",
              image: "alpine:3.18",
              command: ["/bin/sh", "-c", command]
            }],
            restartPolicy: "Never"
          }
        },
        backoffLimit: 0
      }
    };

    const res = await fetch('/apis/batch/v1/namespaces/default/jobs', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(jobManifest)
    });
    
    if (!res.ok) throw new Error(await res.text());

    return new Promise<string>((resolve) => {
      setTimeout(async () => {
        try {
          const podsRes = await fetch(`/api/v1/namespaces/default/pods?labelSelector=job-name=${jobName}`);
          const podsData = await podsRes.json();
          if (podsData.items && podsData.items.length > 0) {
             const podName = podsData.items[0].metadata.name;
             const logsRes = await fetch(`/api/v1/namespaces/default/pods/${podName}/log`);
             const logs = await logsRes.text();
             
             fetch(`/apis/batch/v1/namespaces/default/jobs/${jobName}`, { method: 'DELETE', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ propagationPolicy: "Background" }) });
             
             resolve(logs);
          } else {
             resolve("Command executed but could not fetch logs.");
          }
        } catch(e) {
          resolve("Failed to fetch logs.");
        }
      }, 4000); // give it a few seconds to run ping -c 3
    });
  };

  const deployCustomApp = async (appName: string, nodeName?: string, replicas: number = 1) => {
    try {
      // If deployment already exists, scale it
      const check = await fetch(`/apis/apps/v1/namespaces/default/deployments/${appName}`);
      if (check.ok) {
        const patch = [{ op: 'replace', path: '/spec/replicas', value: replicas }];
        const patchRes = await fetch(`/apis/apps/v1/namespaces/default/deployments/${appName}`, {
          method: 'PATCH',
          headers: { 'Content-Type': 'application/json-patch+json' },
          body: JSON.stringify(patch)
        });
        if (!patchRes.ok) throw new Error(await patchRes.text());
      } else {
        const deployment = {
          apiVersion: 'apps/v1',
          kind: 'Deployment',
          metadata: { name: appName, namespace: 'default' },
          spec: {
            replicas: replicas,
            selector: { matchLabels: { app: appName } },
            template: {
              metadata: { labels: { app: appName } },
              spec: {
                ...(nodeName && { nodeSelector: { 'kubernetes.io/hostname': nodeName } }),
                containers: [{
                  name: 'nginx',
                  image: 'nginx:alpine',
                  ports: [{ containerPort: 80 }]
                }]
              }
            }
          }
        };

        const res = await fetch('/apis/apps/v1/namespaces/default/deployments', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(deployment),
        });
        if (!res.ok) throw new Error(await res.text());
      }
      await fetchClusterState();
    } catch (err: any) {
      setError(err.message);
      throw err;
    }
  };

  const deleteCustomApp = async (appName: string) => {
    try {
      const res = await fetch(`/apis/apps/v1/namespaces/default/deployments/${appName}`, {
        method: 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ propagationPolicy: "Foreground" })
      });
      if (!res.ok && res.status !== 404) throw new Error(await res.text());
      await fetchClusterState();
    } catch (err: any) {
      console.error(err);
      setError(err.message);
    }
  };

  const getPodLogs = async (namespace: string, podName: string) => {
    const res = await fetch(`/api/v1/namespaces/${namespace}/pods/${podName}/log?tailLines=200`);
    if (!res.ok) throw new Error(await res.text());
    return await res.text();
  };

  return {
    nodes,
    pods,
    error,
    operatorStatus,
    deletePod,
    scaleDeployment,
    applyNetworkPolicy,
    deleteNetworkPolicy,
    toggleOperator,
    setIPPoolDisabled,
    createTinyIPPool,
    deleteTinyIPPool,
    patchDeploymentResources,
    updateCoreDNSConfigMap,
    runNodeShellCommand,
    runCommandAndGetLogs,
    deployCustomApp,
    deleteCustomApp,
    getPodLogs
  };
}
