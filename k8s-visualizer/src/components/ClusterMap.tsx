import React, { useEffect, useCallback } from 'react';
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
  <div className="custom-node internet-node">
    <Globe className="node-icon" size={24} />
    <div>
      <strong>{data.label}</strong>
    </div>
    <Handle type="source" position={Position.Bottom} />
  </div>
);

const K8sNodeComponent = ({ data }: { data: any }) => (
  <div className="custom-node k8s-node">
    <Handle type="target" position={Position.Top} />
    <Server className="node-icon" size={20} />
    <div>
      <strong>Node</strong>
      <div className="node-name">{data.label}</div>
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
    >
      <Handle type="target" position={Position.Top} />
      <Box className="node-icon" size={18} />
      <div>
        <strong>{data.isCNI ? 'CNI Agent' : 'Pod'}</strong>
        <div className="node-name" title={data.label}>{data.label.substring(0, 20)}...</div>
      </div>
      <div className={`status-badge ${statusClass}`}>
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
    setNodes((currentNodes) => {
      const newNodes: Node[] = [];
      
      // Keep existing positions
      const getPos = (id: string, defaultX: number, defaultY: number) => {
        const existing = currentNodes.find(n => n.id === id);
        return existing ? existing.position : { x: defaultX, y: defaultY };
      };

      // 1. Internet Node
      newNodes.push({
        id: 'internet',
        type: 'internet',
        position: getPos('internet', 400, 50),
        data: { label: 'External Traffic' },
      });

      // 2. K8s Nodes
      k8sNodes.forEach((node, index) => {
        const xPos = 200 + index * 400;
        newNodes.push({
          id: `node-${node.name}`,
          type: 'k8sNode',
          position: getPos(`node-${node.name}`, xPos, 200),
          data: { label: node.name, status: node.status },
        });
      });

      // 3. K8s Pods
      const podsByNode: Record<string, K8sPod[]> = {};
      k8sPods.forEach(pod => {
        if (!podsByNode[pod.nodeName]) podsByNode[pod.nodeName] = [];
        podsByNode[pod.nodeName].push(pod);
      });

      k8sNodes.forEach((node, nodeIndex) => {
        const nodeXPos = 200 + nodeIndex * 400;
        const podsOnThisNode = podsByNode[node.name] || [];
        
        podsOnThisNode.forEach((pod, podIndex) => {
          const offset = (podIndex - (podsOnThisNode.length - 1) / 2) * 200;
          newNodes.push({
            id: `pod-${pod.name}`,
            type: 'k8sPod',
            position: getPos(`pod-${pod.name}`, nodeXPos + offset, 400),
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

      return newNodes;
    });

    // Edges can be fully rebuilt without losing state
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
      // Draw edge from Node to Pod
      newEdges.push({
        id: `e-${pod.nodeName}-${pod.name}`,
        source: `node-${pod.nodeName}`,
        target: `pod-${pod.name}`,
        animated: pod.status === 'Running',
        style: { stroke: pod.status === 'Terminating' ? '#ef4444' : '#10b981', strokeWidth: 2, opacity: 0.5 },
        markerEnd: { type: MarkerType.ArrowClosed, color: pod.status === 'Terminating' ? '#ef4444' : '#10b981' },
      });
    });

    // Draw all test edges
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
          // 1. Pod A -> CNI A
          newEdges.push({
            id: `e-test-p1-${edge.source}-${cniA.name}-${idx}`,
            source: `pod-${edge.source}`,
            target: `pod-${cniA.name}`,
            animated: !isEdgeCrashing,
            style: baseStyle,
            markerEnd: baseMarker,
          });

          // 2. CNI A -> CNI B
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

          // 3. CNI B -> Pod B
          newEdges.push({
            id: `e-test-p3-${cniB.name}-${edge.target}-${idx}`,
            source: `pod-${cniB.name}`,
            target: `pod-${edge.target}`,
            animated: !isEdgeCrashing,
            style: baseStyle,
            markerEnd: baseMarker,
          });
        } else if (cniA) {
          // CNI B is dead! Draw a severed connection
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
        return; // Do not draw the direct edge!
      }

      // Same-node connection
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

    setEdges(newEdges);
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
      >
        <Background color="#cbd5e1" gap={24} />
        <Controls style={{ background: '#ffffff', border: '1px solid #e2e8f0', fill: '#64748b' }} />
      </ReactFlow>
    </div>
  );
}
