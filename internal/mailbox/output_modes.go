package mailbox

func (a *App) writeSendOutput(format outputFormat, full bool, result SendResult) error {
	if format != outputFormatText {
		if full {
			return a.writeStructuredOutput(format, fullSendResult(result))
		}
		return a.writeStructuredOutput(format, summarizeSendResult(result))
	}
	if full {
		return a.writeSendResultFullText(fullSendResult(result))
	}
	return a.writeSendResultText(summarizeSendResult(result))
}

func (a *App) writeReceiveOutput(format outputFormat, full bool, message ReceivedMessage) error {
	if format != outputFormatText {
		if full {
			return a.writeStructuredOutput(format, message)
		}
		return a.writeStructuredOutput(format, summarizeReceivedMessage(message))
	}
	if full {
		return a.writeReceivedMessageFullText(message)
	}
	return a.writeReceivedMessageText(summarizeReceivedMessage(message))
}

func (a *App) writeGroupReceiveOutput(format outputFormat, full bool, message GroupReceivedMessage) error {
	if format != outputFormatText {
		if full {
			return a.writeStructuredOutput(format, message)
		}
		return a.writeStructuredOutput(format, summarizeGroupReceivedMessage(message))
	}
	if full {
		return a.writeGroupReceivedMessageFullText(message)
	}
	return a.writeGroupReceivedMessageText(summarizeGroupReceivedMessage(message))
}

func (a *App) writeReceiveBatchOutput(format outputFormat, full bool, result ReceiveResult) error {
	if format != outputFormatText {
		if full {
			return a.writeStructuredOutput(format, result)
		}
		return a.writeStructuredOutput(format, summarizeReceiveResult(result))
	}
	if full {
		return a.writeReceiveResultFullText(result)
	}
	return a.writeReceiveResultText(summarizeReceiveResult(result))
}

func (a *App) writeWaitOutput(format outputFormat, full bool, delivery ListedDelivery) error {
	if format != outputFormatText {
		if full {
			return a.writeStructuredOutput(format, delivery)
		}
		return a.writeStructuredOutput(format, summarizeListedDelivery(delivery))
	}
	if full {
		return a.writeListedDeliveryText(delivery)
	}
	return a.writeWaitedDeliveryText(summarizeListedDelivery(delivery))
}

func (a *App) writeGroupWaitOutput(format outputFormat, full bool, message GroupListedMessage) error {
	if format != outputFormatText {
		if full {
			return a.writeStructuredOutput(format, message)
		}
		return a.writeStructuredOutput(format, summarizeGroupListedMessage(message))
	}
	if full {
		return a.writeGroupListedMessageText(message)
	}
	return a.writeGroupWaitedMessageText(summarizeGroupListedMessage(message))
}
