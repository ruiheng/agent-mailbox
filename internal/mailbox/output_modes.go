package mailbox

func (a *App) writeSendOutput(format outputFormat, full bool, result SendResult) error {
	if format != outputFormatText {
		if full {
			return a.writeStructuredOutput(format, FullSendResult(result))
		}
		return a.writeStructuredOutput(format, CompactSendResult(result))
	}
	if full {
		return a.writeSendResultFullText(FullSendResult(result))
	}
	return a.writeSendResultText(CompactSendResult(result))
}

func (a *App) writeReceiveOutput(format outputFormat, full bool, message ReceivedMessage) error {
	if format != outputFormatText {
		if full {
			return a.writeStructuredOutput(format, message)
		}
		return a.writeStructuredOutput(format, CompactReceivedMessage(message))
	}
	if full {
		return a.writeReceivedMessageFullText(message)
	}
	return a.writeReceivedMessageText(CompactReceivedMessage(message))
}

func (a *App) writeGroupReceiveOutput(format outputFormat, full bool, message GroupReceivedMessage) error {
	if format != outputFormatText {
		if full {
			return a.writeStructuredOutput(format, message)
		}
		return a.writeStructuredOutput(format, CompactGroupReceivedMessage(message))
	}
	if full {
		return a.writeGroupReceivedMessageFullText(message)
	}
	return a.writeGroupReceivedMessageText(CompactGroupReceivedMessage(message))
}

func (a *App) writeReceiveBatchOutput(format outputFormat, full bool, result ReceiveResult) error {
	if format != outputFormatText {
		if full {
			return a.writeStructuredOutput(format, result)
		}
		return a.writeStructuredOutput(format, CompactReceiveResult(result))
	}
	if full {
		return a.writeReceiveResultFullText(result)
	}
	return a.writeReceiveResultText(CompactReceiveResult(result))
}

func (a *App) writeWaitOutput(format outputFormat, full bool, delivery ListedDelivery) error {
	if format != outputFormatText {
		if full {
			return a.writeStructuredOutput(format, delivery)
		}
		return a.writeStructuredOutput(format, CompactListedDelivery(delivery))
	}
	if full {
		return a.writeListedDeliveryText(delivery)
	}
	return a.writeWaitedDeliveryText(CompactListedDelivery(delivery))
}

func (a *App) writeGroupWaitOutput(format outputFormat, full bool, message GroupListedMessage) error {
	if format != outputFormatText {
		if full {
			return a.writeStructuredOutput(format, message)
		}
		return a.writeStructuredOutput(format, CompactGroupListedMessage(message))
	}
	if full {
		return a.writeGroupListedMessageText(message)
	}
	return a.writeGroupWaitedMessageText(CompactGroupListedMessage(message))
}
