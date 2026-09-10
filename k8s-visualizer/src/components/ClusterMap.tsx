import { useEffect } from 'react';
import {
  ReactFlow,
  Background,
  Controls,
  MarkerType,
  Handle,
  Position,
  useNodesState,
  useEdgesState,
} from '@xyflow/react';
import type { Node, Edge } from '@xyflow/react';
import '@xyflow/react/dist/style.css';
import type { K8sNode, K8sPod } from '../hooks/useKubernetes';
import { Server, Box, Globe } from 'lucide-react';

const InternetNode = ({ data }: { data: any }) => (
  <div className="custom-node internet-node" style={{ width: '180px', padding: '12px' }}>
    <Globe className="node-icon" size={24} />
    <div>
      <strong style={{ fontSize: '0.85rem' }}>{data.label}</strong>
    </div>
    <Handle type="source" position={Position.Bottom} />
  </div>
);

const K8sNodeComponent = ({ data }: { data: any }) => (
  <div className="custom-node k8s-node" style={{ width: '220px', padding: '14px' }}>
    <Handle type="target" position={Position.Top} />
    <Server className="node-icon" size={22} color="#3b82f6" />
    <div>
      <strong style={{ fontSize: '0.9rem' }}>Node</strong>
      <div className="node-name" style={{ fontSize: '0.8rem', fontWeight: 600 }}>{data.label}</div>
    </div>
    <div className={`status-badge ${data.status.toLowerCase()}`}>
      {data.status}
    </div>
    <Handle type="source" position={Position.Bottom} />
  </div>
);

const K8sPodComponent = ({ data }: { data: any }) => {
  let statusClass = 'healthy';
  if (data.status === 'Terminating') statusClass = 'terminating';
  if (data.status === 'NotReady' || data.status === 'Pending') statusClass = 'pending';

  const isSelectedClass = data.selected ? 'selected' : '';

  return (
    <div 
      className={`custom-node k8s-pod ${statusClass} ${isSelectedClass}`}
      onClick={() => data.onSelect && data.onSelect(data.label)}
      style={{ width: '190px', padding: '10px 12px', boxSizing: 'border-box' }}
    >
      <Handle type="target" position={Position.Top} />
      <Box className="node-icon" size={16} />
      <div style={{ width: '100%', textOverflow: 'ellipsis', overflow: 'hidden' }}>
        <strong style={{ fontSize: '0.78rem' }}>{data.isCNI ? 'CNI Agent' : 'Pod'}</strong>
        <div 
          className="node-name" 
          title={data.label} 
          style={{ fontSize: '0.72rem', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', marginTop: '2px' }}
        >
          {data.label}
        </div>
      </div>
      <div className={`status-badge ${statusClass}`} style={{ fontSize: '0.65rem', padding: '2px 8px', marginTop: '4px' }}>
        {data.status}
      </div>
      {(data.isSource || data.isTarget) && (
        <div className="selection-badge">
          {data.isSource ? 'Source' : data.isTarget ? 'Target' : 'Selected'}
        </div>
      )}
      <Handle type="source" position={Position.Bottom} />
    </div>
  );
};

const nodeTypes = {
  internet: InternetNode,
  k8sNode: K8sNodeComponent,
  k8sPod: K8sPodComponent,
};

interface ClusterMapProps {
  k8sNodes: K8sNode[];
  k8sPods: K8sPod[];
  isCrashing: boolean;
  testEdges: { source: string; target: string }[];
  pendingSource: string | null;
  selectedPod: string | null;
  onSelectPod: (podName: string) => void;
}

export function ClusterMap({ k8sNodes, k8sPods, isCrashing, testEdges, pendingSource, selectedPod, onSelectPod }: ClusterMapProps) {
  const [nodes, setNodes, onNodesChange] = useNodesState<Node>([]);
  const [edges, setEdges, onEdgesChange] = useEdgesState<Edge>([]);

  useEffect(() => {
    // Recompute clean 2D grid layout to prevent pod overlaps
    const newNodes: Node[] = [];

    // Calculate internet node position centered over worker nodes
    const totalNodesCount = Math.max(k8sNodes.length, 1);
    const centerInternetX = 200 + ((totalNodesCount - 1) * 600) / 2;

    // 1. Internet Node
    newNodes.push({
      id: 'internet',
      type: 'internet',
      position: { x: centerInternetX, y: 30 },
      data: { label: 'External Traffic' },
    });

    // 2. K8s Nodes
    k8sNodes.forEach((node, index) => {
      const xPos = 200 + index * 600;
      newNodes.push({
        id: `node-${node.name}`,
        type: 'k8sNode',
        position: { x: xPos, y: 160 },
        data: { label: node.name, status: node.status },
      });
    });

    // 3. K8s Pods (Organized in 2-column grid per node)
    const podsByNode: Record<string, K8sPod[]> = {};
    k8sPods.forEach(pod => {
      if (!podsByNode[pod.nodeName]) podsByNode[pod.nodeName] = [];
      podsByNode[pod.nodeName].push(pod);
    });

    k8sNodes.forEach((node, nodeIndex) => {
      const nodeXCenter = 200 + nodeIndex * 600;
      const podsOnThisNode = podsByNode[node.name] || [];
      
      const COLS = 2; // 2 columns per node
      const COLUMN_SPACING = 215; // horizontal distance between columns
      const ROW_SPACING = 110;    // vertical distance between rows
      
      podsOnThisNode.forEach((pod, podIndex) => {
        const col = podIndex % COLS;
        const row = Math.floor(podIndex / COLS);
        
        // Calculate grid x relative to node center
        const xOffset = (col - (COLS - 1) / 2) * COLUMN_SPACING;
        const posX = nodeXCenter + xOffset;
        const posY = 320 + row * ROW_SPACING;

        newNodes.push({
          id: `pod-${pod.name}`,
          type: 'k8sPod',
          position: { x: posX, y: posY },
          data: { 
            label: pod.name, 
            status: pod.status,
            isCNI: pod.isCNI,
            selected: selectedPod === pod.name || pendingSource === pod.name,
            isSource: pendingSource === pod.name,
            isTarget: false,
            onSelect: onSelectPod
          },
        });
      });
    });

    setNodes((nds) => {
      return newNodes.map(newNode => {
        const existingNode = nds.find(n => n.id === newNode.id);
        if (existingNode) {
          // Preserve React Flow's internal properties (measured, width, height) to prevent crash
          return { ...existingNode, ...newNode, data: { ...existingNode.data, ...newNode.data } };
        }
        return newNode;
      });
    });

    // Edges
    const newEdges: Edge[] = [];
    k8sNodes.forEach(node => {
      newEdges.push({
        id: `e-internet-${node.name}`,
        source: 'internet',
        target: `node-${node.name}`,
        animated: true,
        style: { stroke: '#3b82f6', strokeWidth: 2 },
        markerEnd: { type: MarkerType.ArrowClosed, color: '#3b82f6' },
      });
    });

    k8sPods.forEach(pod => {
      if (!pod.nodeName || !k8sNodes.some(n => n.name === pod.nodeName)) return; // Skip edges for pending/unassigned pods

      newEdges.push({
        id: `e-${pod.nodeName}-${pod.name}`,
        source: `node-${pod.nodeName}`,
        target: `pod-${pod.name}`,
        animated: pod.status === 'Running',
        style: { stroke: pod.status === 'Terminating' ? '#ef4444' : '#10b981', strokeWidth: 2, opacity: 0.5 },
        markerEnd: { type: MarkerType.ArrowClosed, color: pod.status === 'Terminating' ? '#ef4444' : '#10b981' },
      });
    });

    // Test edges
    testEdges.forEach((edge, idx) => {
      const isEdgeCrashing = isCrashing && (selectedPod === edge.source || selectedPod === edge.target);
      const baseStyle = { stroke: isEdgeCrashing ? '#ef4444' : '#eab308', strokeWidth: 4 };
      const baseMarker = { type: MarkerType.ArrowClosed, color: isEdgeCrashing ? '#ef4444' : '#eab308' };

      const sourcePodObj = k8sPods.find(p => p.name === edge.source);
      const targetPodObj = k8sPods.find(p => p.name === edge.target);

      if (sourcePodObj && targetPodObj && sourcePodObj.nodeName !== targetPodObj.nodeName) {
        const cniA = k8sPods.find(p => p.isCNI && p.nodeName === sourcePodObj.nodeName);
        const cniB = k8sPods.find(p => p.isCNI && p.nodeName === targetPodObj.nodeName);

        if (cniA && cniB) {
          // Pod A -> CNI A
          newEdges.push({
            id: `e-test-p1-${edge.source}-${cniA.name}-${idx}`,
            source: `pod-${edge.source}`,
            target: `pod-${cniA.name}`,
            animated: !isEdgeCrashing,
            style: baseStyle,
            markerEnd: baseMarker,
          });

          // CNI A -> CNI B
          newEdges.push({
            id: `e-test-p2-${cniA.name}-${cniB.name}-${idx}`,
            source: `pod-${cniA.name}`,
            target: `pod-${cniB.name}`,
            animated: !isEdgeCrashing,
            style: baseStyle,
            markerEnd: baseMarker,
            label: isEdgeCrashing ? 'Tunnel Broken' : 'CNI Tunnel',
            labelBgPadding: [8, 4],
            labelBgBorderRadius: 4,
            labelBgStyle: { fill: '#fff', fillOpacity: 0.8 },
          });

          // CNI B -> Pod B
          newEdges.push({
            id: `e-test-p3-${cniB.name}-${edge.target}-${idx}`,
            source: `pod-${cniB.name}`,
            target: `pod-${edge.target}`,
            animated: !isEdgeCrashing,
            style: baseStyle,
            markerEnd: baseMarker,
          });
        } else if (cniA) {
          newEdges.push({
            id: `e-test-p1-${edge.source}-${cniA.name}-${idx}`,
            source: `pod-${edge.source}`,
            target: `pod-${cniA.name}`,
            animated: false,
            style: { stroke: '#ef4444', strokeWidth: 4 },
            markerEnd: { type: MarkerType.ArrowClosed, color: '#ef4444' },
            label: 'Tunnel Severed',
            labelBgPadding: [8, 4],
            labelBgBorderRadius: 4,
            labelBgStyle: { fill: '#fff', fillOpacity: 0.8 },
          });
        }
        return;
      }

      newEdges.push({
        id: `e-test-${edge.source}-${edge.target}-${idx}`,
        source: `pod-${edge.source}`,
        target: `pod-${edge.target}`,
        animated: !isEdgeCrashing,
        style: baseStyle,
        markerEnd: baseMarker,
        label: isEdgeCrashing ? 'Connection Broken' : 'Testing Connection',
        labelBgPadding: [8, 4],
        labelBgBorderRadius: 4,
        labelBgStyle: { fill: '#fff', fillOpacity: 0.8 },
      });
    });

    // CRITICAL FIX: React Flow crashes if any edge references a non-existent node.
    // Filter out any edge whose source or target isn't in our newNodes list.
    const validNodeIds = new Set(newNodes.map(n => n.id));
    const safeEdges = newEdges.filter(e => validNodeIds.has(e.source) && validNodeIds.has(e.target));

    setEdges(safeEdges);
  }, [k8sNodes, k8sPods, isCrashing, testEdges, pendingSource, selectedPod, onSelectPod, setNodes, setEdges]);

  return (
    <div style={{ width: '100%', height: '100vh', background: 'var(--bg)' }}>
      <ReactFlow
        nodes={nodes}
        edges={edges}
        onNodesChange={onNodesChange}
        onEdgesChange={onEdgesChange}
        nodeTypes={nodeTypes}
        fitView
        fitViewOptions={{ padding: 0.2 }}
      >
        <Background color="#cbd5e1" gap={24} />
        <Controls style={{ background: '#ffffff', border: '1px solid #e2e8f0', fill: '#64748b' }} />
      </ReactFlow>
    </div>
  );
}
