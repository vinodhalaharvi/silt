static const struct of_device_id mcp251x_of_match[] = {
	{ .compatible = "microchip,mcp2510", .data = (void *)CAN_MCP251X_MCP2510 },
	{ .compatible = "microchip,mcp2515", .data = (void *)CAN_MCP251X_MCP2515 },
	{ }
};
