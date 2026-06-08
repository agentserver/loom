param(
    [string]$ServiceName = "loom-slave",
    [string]$Config = ""
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$exePath = Join-Path $here "slave-agent.exe"
$configPath = if ($Config -ne "") {
    [System.IO.Path]::GetFullPath($Config)
} else {
    Join-Path $here "config.yaml"
}
$logPath = Join-Path $here "slave.log"

if (-not (Test-Path -LiteralPath $exePath -PathType Leaf)) {
    throw "Missing slave-agent.exe next to service wrapper: $exePath"
}
if (-not (Test-Path -LiteralPath $configPath -PathType Leaf)) {
    throw "Missing config file: $configPath"
}

$source = @"
using System;
using System.Diagnostics;
using System.IO;
using System.ServiceProcess;

public sealed class LoomSlaveService : ServiceBase
{
    private readonly string exePath;
    private readonly string configPath;
    private readonly string logPath;
    private Process child;
    private bool stopping;

    public LoomSlaveService(string serviceName, string exePath, string configPath, string logPath)
    {
        ServiceName = serviceName;
        this.exePath = exePath;
        this.configPath = configPath;
        this.logPath = logPath;
        CanStop = true;
        AutoLog = true;
    }

    protected override void OnStart(string[] args)
    {
        Directory.CreateDirectory(Path.GetDirectoryName(logPath));
        var psi = new ProcessStartInfo();
        psi.FileName = exePath;
        psi.Arguments = Quote(configPath);
        psi.WorkingDirectory = Path.GetDirectoryName(exePath);
        psi.UseShellExecute = false;
        psi.CreateNoWindow = true;
        psi.RedirectStandardOutput = true;
        psi.RedirectStandardError = true;

        child = new Process();
        child.StartInfo = psi;
        child.EnableRaisingEvents = true;
        child.OutputDataReceived += delegate(object sender, DataReceivedEventArgs e) { AppendLine(e.Data); };
        child.ErrorDataReceived += delegate(object sender, DataReceivedEventArgs e) { AppendLine(e.Data); };
        child.Exited += delegate(object sender, EventArgs e) {
            AppendLine("slave-agent exited with code " + child.ExitCode);
            if (!stopping)
            {
                ExitCode = child.ExitCode == 0 ? 1 : child.ExitCode;
                Stop();
            }
        };
        child.Start();
        child.BeginOutputReadLine();
        child.BeginErrorReadLine();
    }

    protected override void OnStop()
    {
        stopping = true;
        if (child == null || child.HasExited)
        {
            return;
        }
        try
        {
            child.CloseMainWindow();
            if (!child.WaitForExit(5000))
            {
                child.Kill();
                child.WaitForExit(5000);
            }
        }
        catch
        {
            try { child.Kill(); } catch { }
        }
    }

    private static string Quote(string value)
    {
        return "\"" + value.Replace("\"", "\\\"") + "\"";
    }

    private void AppendLine(string line)
    {
        if (line == null)
        {
            return;
        }
        File.AppendAllText(logPath, DateTimeOffset.Now.ToString("o") + " " + line + Environment.NewLine);
    }
}
"@

Add-Type -TypeDefinition $source -ReferencedAssemblies "System.ServiceProcess.dll"
[System.ServiceProcess.ServiceBase]::Run([LoomSlaveService]::new($ServiceName, $exePath, $configPath, $logPath))
