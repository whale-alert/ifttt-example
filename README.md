# ifttt-example

## Overview
This Go application is a simple WebSocket client that connects to the [Whale Alert WebSocket API](https://whale-alert.io/documentation/#websocket-about) to receive real-time alerts for cryptocurrency transactions. It integrates with [IFTTT](https://ifttt.com) by using [webhooks](https://ifttt.com/maker_webhooks) based on the alerts received. This code accompanies the tutorial available [here](https://medium.com/p/5932fcb5783e).

## Features
- Connects to Whale Alert's WebSocket API to receive real-time alerts.
- Subscribes to specific blockchains, symbols, and transaction types.
- Rate-limits IFTTT webhook calls to avoid exceeding API limits.
- Gracefully handles WebSocket reconnections.
- Logs alerts and errors for debugging and monitoring.

## Prerequisites
- Go 1.16 or higher
- Whale Alert API key
- IFTTT API key

## Installation
1. Clone the repository:
```
git clone https://github.com/whale-alert/ifttt-example.git
```

2. Navigate to the project directory:
```
cd ifttt-example
```

3. Install the required Go packages:
```
go mod download
```

## Configuration
Replace the following placeholders in the code with your actual API keys:

- YOUR_WHALE_ALERT_API_KEY: Your Whale Alert API key.
- YOUR_IFTTT_API_KEY: Your IFTTT API key.
  
You can also modify the subscription variable to customize the alerts you want to subscribe to.

## Usage
1. Build the project:
```
go build
```

2. Run the executable:
```
./whale-alert-websocket-client
```

## Contributing
Feel free to open issues or submit pull requests to improve the project.

## License
This project is licensed under the MIT License.
