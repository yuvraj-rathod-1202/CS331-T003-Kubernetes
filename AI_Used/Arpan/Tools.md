# Tools

**Tool used:** Antigravity IDE with Gemini 3.1 Pro and Claude models.
**Prompt Refiner**: I have a local tool which converts the vague prompts into high class prompt using prompt enginnering techniques. So all the prompts given in the Prompts.md are actually the vague one and then this tool is attached in the pipeline which converts the prompt in between and that will passed to the Models. 

**What it is:** Antigravity IDE is an AI-powered coding assistant deeply integrated into the development environment. It can read and edit files, execute shell commands, interact with Kubernetes clusters, and assist with debugging and code generation in real-time.

**How I accessed it:** Through the Antigravity IDE interface on my macOS setup, connected to my local development environment where the project repository was open.

**Where I used it:** Primarily for my module, the **CoreDNS Remediation** module. I used it to:
- Implement the CoreDNS controller logic (`operator/internal/controller/coredns/controller.go`).
- Debug and fix issues with the CoreDNS remediation operator.
- Fix simulation bugs in the k8s-visualizer frontend.
- Generate documentation and workflow diagrams for the CoreDNS module.
- Fix lint errors and code quality issues.
- Review and verify code against the project description for completeness.

I have also use other standalone tools like ChatGPT for this project's implementation.
