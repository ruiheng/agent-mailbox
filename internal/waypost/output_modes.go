package waypost

func writeFormattedOutput[T any](a *App, format outputFormat, value T, writeText func(T) error) error {
	if format != outputFormatText {
		return a.writeStructuredOutput(format, value)
	}
	return writeText(value)
}

func (a *App) writeSendOutput(format outputFormat, full bool, result SendResult) error {
	if full {
		return writeFormattedOutput(a, format, FullSendResult(result), a.writeSendResultFullText)
	}
	return writeFormattedOutput(a, format, CompactSendResult(result), a.writeSendResultText)
}

func (a *App) writeForwardOutput(format outputFormat, full bool, result ForwardResult) error {
	if full {
		return writeFormattedOutput(a, format, FullForwardResult(result), a.writeForwardResultFullText)
	}
	return writeFormattedOutput(a, format, CompactForwardResult(result), a.writeForwardResultText)
}

func (a *App) writeReceiveOutput(format outputFormat, full bool, message ReceivedMessage) error {
	if full {
		return writeFormattedOutput(a, format, message, a.writeReceivedMessageFullText)
	}
	return writeFormattedOutput(a, format, CompactReceivedMessage(message), a.writeReceivedMessageText)
}

func (a *App) writeGroupReceiveOutput(format outputFormat, full bool, message GroupReceivedMessage) error {
	compact := CompactGroupReceivedMessage(message)
	if full {
		if format != outputFormatText {
			return a.writeStructuredOutput(format, message)
		}
		return a.writeGroupReceivedMessageText(compact)
	}
	return writeFormattedOutput(a, format, compact, a.writeGroupReceivedMessageText)
}

func (a *App) writeReceiveBatchOutput(format outputFormat, full bool, result ReceiveResult) error {
	if full {
		return writeFormattedOutput(a, format, result, a.writeReceiveResultFullText)
	}
	return writeFormattedOutput(a, format, CompactReceiveResult(result), a.writeReceiveResultText)
}

func (a *App) writeWaitOutput(format outputFormat, full bool, delivery ListedDelivery) error {
	if full {
		return writeFormattedOutput(a, format, delivery, a.writeListedDeliveryText)
	}
	return writeFormattedOutput(a, format, CompactListedDelivery(delivery), a.writeWaitedDeliveryText)
}

func (a *App) writeGroupWaitOutput(format outputFormat, full bool, message GroupListedMessage) error {
	if full {
		return writeFormattedOutput(a, format, message, a.writeGroupListedMessageText)
	}
	return writeFormattedOutput(a, format, CompactGroupListedMessage(message), a.writeGroupWaitedMessageText)
}
