package discord

import (
	"encoding/json"
	"time"

	"github.com/bwmarrin/discordgo"
)

// handleAnalysis triggers a live LLM analysis and waits for the result.
func (b *Bot) handleAnalysis(s *discordgo.Session, i *discordgo.InteractionCreate) {
	respondDeferred(s, i)

	startTime := time.Now()

	if err := b.client.TriggerPoll(); err != nil {
		// Check if analysis is already in progress — show latest instead.
		results, getErr := b.client.GetAnalysis(1)
		if getErr == nil && len(results) > 0 {
			editResponse(s, i, []*discordgo.MessageEmbed{
				analysisEmbed(&results[0]),
			})
			return
		}
		editResponse(s, i, []*discordgo.MessageEmbed{
			errorEmbed("Analysis Error", err.Error()),
		})
		return
	}

	result, err := b.pollForAnalysis(startTime)
	if err != nil {
		// Timeout — show the latest available analysis.
		results, getErr := b.client.GetAnalysis(1)
		if getErr == nil && len(results) > 0 {
			embed := analysisEmbed(&results[0])
			embed.Footer = &discordgo.MessageEmbedFooter{
				Text: "⚠️ Timed out waiting for fresh analysis — showing most recent",
			}
			editResponse(s, i, []*discordgo.MessageEmbed{embed})
			return
		}
		editResponse(s, i, []*discordgo.MessageEmbed{
			errorEmbed("Analysis Timeout", "Analysis did not complete within 3 minutes."),
		})
		return
	}

	editResponse(s, i, []*discordgo.MessageEmbed{analysisEmbed(result)})
}

// handleStatus shows current task status grouped by state.
func (b *Bot) handleStatus(s *discordgo.Session, i *discordgo.InteractionCreate) {
	tasks, err := b.client.GetTasks()
	if err != nil {
		respond(s, i, []*discordgo.MessageEmbed{
			errorEmbed("Status Error", err.Error()),
		})
		return
	}

	respond(s, i, []*discordgo.MessageEmbed{statusEmbed(tasks)})
}

// handleSettings shows autopilot and review configuration (ephemeral).
func (b *Bot) handleSettings(s *discordgo.Session, i *discordgo.InteractionCreate) {
	status, err := b.client.GetStatus()
	if err != nil {
		respondEphemeral(s, i, []*discordgo.MessageEmbed{
			errorEmbed("Settings Error", err.Error()),
		})
		return
	}

	respondEphemeral(s, i, []*discordgo.MessageEmbed{settingsEmbed(status)})
}

// handleCost shows cost overview with daily, weekly, and project totals.
func (b *Bot) handleCost(s *discordgo.Session, i *discordgo.InteractionCreate) {
	metrics, err := b.client.GetMetricsRaw()
	if err != nil {
		respond(s, i, []*discordgo.MessageEmbed{
			errorEmbed("Cost Error", err.Error()),
		})
		return
	}

	var m MetricsResponse
	if err := json.Unmarshal(metrics, &m); err != nil {
		respond(s, i, []*discordgo.MessageEmbed{
			errorEmbed("Cost Error", "Failed to parse metrics: "+err.Error()),
		})
		return
	}

	respond(s, i, []*discordgo.MessageEmbed{costEmbed(&m)})
}
