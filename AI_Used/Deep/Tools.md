# Tools

**Tool used:** Antigravity IDE with Gemini 3.1 Pro (and occasionally Claude Opus) models.

**What it is:** Antigravity IDE is an AI coding assistant deeply integrated into the development environment. It can read, edit files, run shell commands, and interact with Kubernetes clusters directly. 

**How I accessed it:** Through the IDE interface connected to my local WSL/Windows environment in the project directory.

**Where I used it:** Primarily for my module, the **Pod-Connectivity** module. I used it to:
- Debug and fix Python-based ping probe execution errors.
- Correct the node triangulation logic.
- Implement proper taint removal on network remediation.


I did not use other standalone chat tools like ChatGPT, as the integrated IDE allowed for direct execution and testing against my local Minikube cluster.
