# How to Run the Project

To run the full project (Operator + Visualizer + K8s API Proxy), you need to open **3 separate terminals** and run the following commands.

### Terminal 1: Run the K8s Proxy
This exposes the Kubernetes API so the frontend can interact with it.
```bash
# Wait for your cluster to be running, then:
kubectl proxy
```

### Terminal 2: Run the Kubernetes Operator
This runs the custom controller that monitors network failures and remediates them.
```bash
cd operator
make run
```

### Terminal 3: Run the Visualizer Frontend
This runs the React web interface.
```bash
cd k8s-visualizer
npm run dev
```
