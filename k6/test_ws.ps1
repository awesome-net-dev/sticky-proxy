param(
    [string]$jwt,
    [string]$uri = "ws://localhost:8080/ws"
)

Add-Type @"
using System;
using System.Net.WebSockets;
using System.Text;
using System.Threading;
using System.Threading.Tasks;

public class WSClient {
    public static async Task Connect(string uri, string jwt) {
        using (var ws = new ClientWebSocket()) {
            ws.Options.SetRequestHeader("Authorization", "Bearer " + jwt);
            await ws.ConnectAsync(new Uri(uri), CancellationToken.None);

            var message = "Hello from PowerShell";
            var bytes = Encoding.UTF8.GetBytes(message);
            await ws.SendAsync(new ArraySegment<byte>(bytes), WebSocketMessageType.Text, true, CancellationToken.None);

            var buffer = new byte[1024];
            var result = await ws.ReceiveAsync(new ArraySegment<byte>(buffer), CancellationToken.None);
            Console.WriteLine("Received: " + Encoding.UTF8.GetString(buffer, 0, result.Count));

            await ws.CloseAsync(WebSocketCloseStatus.NormalClosure, "Done", CancellationToken.None);
        }
    }
}
"@

try {
    [WSClient]::Connect($uri, $jwt).Wait()
} catch [System.AggregateException] {
    $_.Exception.InnerExceptions | ForEach-Object { Write-Host $_.Message }
}